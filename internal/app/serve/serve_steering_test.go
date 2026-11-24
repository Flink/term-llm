package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/serve/wire"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/providers/opencompat"
	"github.com/term-llm/term-llm/internal/session"
)

// No special Rush capability: wrappers and native adapters use ordinary Stream.
type rushContinuationProvider struct{ core.Provider }

// Track local stream settlement rather than when the remote HTTP handler is
// scheduled to observe cancellation. Closing a response body cannot acknowledge
// that remote observation.
type rushHTTPSettlementProvider struct {
	core.Provider
	calls   atomic.Int32
	stopped chan struct{}
	// opened closes once the source Stream call has returned a live stream.
	// The server flushing its first chunk does not mean the client has read the
	// response headers; rushing before that cancels the open itself, leaving no
	// source stream whose Close the replacement could be ordered after.
	opened chan struct{}
	t      *testing.T
}

func (p *rushHTTPSettlementProvider) Stream(ctx context.Context, req core.Request) (core.Stream, error) {
	first := p.calls.Add(1) == 1
	if !first {
		select {
		case <-p.stopped:
		default:
			p.t.Error("replacement started before source stream Close returned")
		}
	}
	s, err := p.Provider.Stream(ctx, req)
	if err != nil || !first {
		return s, err
	}
	close(p.opened)
	return &rushHTTPSettlementStream{Stream: s, stopped: p.stopped}, nil
}

type rushHTTPSettlementStream struct {
	core.Stream
	once    sync.Once
	stopped chan struct{}
}

func (s *rushHTTPSettlementStream) Close() error {
	err := s.Stream.Close()
	s.once.Do(func() { close(s.stopped) })
	return err
}

func TestRushHTTPSettlesThenStartsExactlyOneOrderedReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wrapped bool
		partial bool
	}{
		{name: "direct", partial: true},
		{name: "provider-without-special-rush-interface", wrapped: true, partial: true},
		{name: "before-first-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{})
			sourceStopped := make(chan struct{})
			replacementInput := make(chan []map[string]any, 1)
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					// Cover cancellation before the first token as well as after a
					// partial response. Flushing headers alone opens a live stream.
					if tc.partial {
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
					}
					w.(http.Flusher).Flush()
					close(entered)
					<-r.Context().Done()
					close(sourceStopped)
					return
				}
				replacementInput <- body.Messages
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"steered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer model.Close()
			store, err := session.NewSQLiteStore(session.Config{Enabled: true, Path: ":memory:"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sess := &session.Session{ID: "rush-session", Provider: "compat", Model: "test", Mode: session.ModeChat}
			if err := store.Create(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			settlement := &rushHTTPSettlementProvider{
				Provider: opencompat.NewOpenAICompatProvider(model.URL, "", "test", "compat"),
				stopped:  make(chan struct{}),
				opened:   make(chan struct{}),
				t:        t,
			}
			var provider core.Provider = settlement
			if tc.wrapped {
				provider = rushContinuationProvider{provider}
			}
			registry := engine.NewToolRegistry()
			registry.Register(responseTimeoutDelayTool{})
			rt := &serveRuntime{provider: provider, providerKey: "compat", engine: engine.NewEngine(provider, registry), defaultModel: "test", store: store}
			rt.Touch()
			manager := newServeSessionManager(time.Minute, 10, func(context.Context) (*serveRuntime, error) { return rt, nil })
			defer manager.Close()
			manager.sessions[sess.ID] = rt
			srv := &serveServer{store: store, sessionMgr: manager, responseRuns: newServeResponseRunManager(), shutdownCh: make(chan struct{})}
			defer srv.responseRuns.Close()
			defer func() {
				if srv.responseLifecycleCancel != nil {
					srv.responseLifecycleCancel()
					srv.responseLifecycleWG.Wait()
				}
			}()
			const sourceText = "original user prompt"
			partialSeen := make(chan struct{}, 1)
			run, err := srv.startResponseRun(rt, true, false, []core.Message{core.UserText(sourceText)}, core.Request{SessionID: sess.ID, Tools: []core.ToolSpec{responseTimeoutDelayTool{}.Spec()}}, sess.ID, startResponseRunOptions{
				uiSession: true,
				onEvent: func(ev core.Event) error {
					if ev.Type == core.EventTextDelta && ev.Text == "partial" {
						partialSeen <- struct{}{}
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("provider not started")
			}
			select {
			case <-settlement.opened:
			case <-time.After(3 * time.Second):
				t.Fatal("source stream not opened")
			}
			if tc.partial {
				select {
				case <-partialSeen:
				case <-time.After(3 * time.Second):
					t.Fatal("source text not received")
				}
			}
			for _, id := range []string{"z", "a"} {
				_, _, err := rt.InterruptMessage(context.Background(), core.UserText("guidance "+id), "guidance "+id, id, nil, interruptDeliverySteer)
				if err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(map[string]any{"request_id": "rush-once", "expected_response_id": run.id, "expected_run_epoch": run.runEpoch})
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/steering/rush", strings.NewReader(string(body)))
			response := httptest.NewRecorder()
			srv.handleSteeringRush(response, req, sess.ID, "")
			if response.Code != 202 {
				t.Fatalf("admission %d: %s", response.Code, response.Body.String())
			}
			var op *session.RushOperation
			waitForServeCondition(t, 3*time.Second, func() bool {
				var err error
				op, err = store.GetRush(t.Context(), sess.ID, "rush-once")
				if err != nil {
					t.Fatalf("read admitted rush: %v", err)
				}
				return !op.Status.Active()
			}, "rush to start")
			if op.Status != session.RushStarted {
				t.Fatalf("rush = %+v", op)
			}
			select {
			case messages := <-replacementInput:
				raw, _ := json.Marshal(messages)
				text := string(raw)
				source, z, a := strings.Index(text, sourceText), strings.Index(text, "guidance z"), strings.Index(text, "guidance a")
				if source < 0 || z <= source || a <= z {
					t.Fatalf("replacement lost the original prompt or FIFO steering order: %s", text)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("replacement not started")
			}
			// The server must eventually observe cancellation, but that observation
			// need not precede the next request arriving on a different connection.
			select {
			case <-sourceStopped:
			case <-time.After(3 * time.Second):
				t.Fatal("source HTTP request was not cancelled")
			}
			retry := httptest.NewRecorder()
			srv.handleSteeringRush(retry, httptest.NewRequest(http.MethodPost, req.URL.String(), strings.NewReader(string(body))), sess.ID, "")
			if retry.Code != 200 {
				t.Fatalf("retry %d: %s", retry.Code, retry.Body.String())
			}
			if calls.Load() != 2 {
				t.Fatalf("provider calls = %d", calls.Load())
			}
			rows, err := store.GetMessages(context.Background(), sess.ID, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]int{}
			sourceCount := 0
			for _, row := range rows {
				ids[row.ClientMessageID]++
				if row.Role == core.RoleUser && row.TextContent == sourceText {
					sourceCount++
				}
			}
			if sourceCount != 1 || ids["z"] != 1 || ids["a"] != 1 {
				t.Fatalf("durable identities: source=%d, steering=%+v", sourceCount, ids)
			}
		})
	}
}

func TestSteeringWireProjectionPreservesUserContentAndSequence(t *testing.T) {
	event := responseRunEvent{Sequence: 42, Event: "response.interjection", Data: []byte(`{"type":"response.interjection","interjection_id":"id","text":"interject literally","sequence_number":42}`)}
	for _, canonical := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		writer := wire.NewSteeringWriter(recorder, canonical)
		if err := writeStoredResponseEvent(writer, event); err != nil {
			t.Fatal(err)
		}
		want := "response.interjection"
		if canonical {
			want = "response.steering"
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "event: "+want) || !strings.Contains(body, "id: 42") || !strings.Contains(body, "interject literally") {
			t.Fatalf("projection: %s", body)
		}
	}
}
