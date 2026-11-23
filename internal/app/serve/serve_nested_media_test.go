package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/inspect"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/llm/providers/responses"
	"github.com/term-llm/term-llm/internal/tools"
)

const nestedMediaCallID = "script-call"

var nestedMediaURL = regexp.MustCompile(`term-llm-media://([0-9a-fA-F]{32})`)

func nestedMediaToolText(items []responses.ResponsesInputItem) string {
	for _, item := range items {
		if item.Type == "function_call_output" && item.CallID == nestedMediaCallID {
			return item.Output
		}
	}
	return ""
}

// On the second turn, embed only URLs present in the model-facing tool result.
// Do not look at tool.Media or the server's publisher to construct the reply.
type nestedMediaReplyProvider struct{ *mockprovider.MockProvider }

func (p nestedMediaReplyProvider) Stream(ctx context.Context, req core.Request) (core.Stream, error) {
	if p.CurrentTurn() == 1 {
		var reply []string
		out := nestedMediaToolText(responses.BuildResponsesContinuationInput(req.Messages))
		for _, url := range nestedMediaURL.FindAllString(out, -1) {
			reply = append(reply, "![Figure]("+url+")")
		}
		p.AddTextResponse(strings.Join(reply, "\n\n"))
	}
	return p.MockProvider.Stream(ctx, req)
}

// Compare the model's actual function_call_output with what a browser can resolve
// from the live event and from SQLite without the originating runtime or files.
func TestNestedShowMediaLiveAndReload(t *testing.T) {
	srv, store, sess := newTranscriptHandlerServer(t)
	srv.mediaPublisher = &serveMediaPublisher{dir: t.TempDir()}
	sourceDir := t.TempDir()
	var figures [9][]byte
	for i := range figures {
		var pngData bytes.Buffer
		if err := png.Encode(&pngData, image.NewGray(image.Rect(0, 0, i+1, 1))); err != nil {
			t.Fatal(err)
		}
		figures[i] = pngData.Bytes()
		if err := os.WriteFile(filepath.Join(sourceDir, fmt.Sprintf("%d.png", i)), figures[i], 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := tools.DefaultToolConfig()
	cfg.Enabled = []string{"spawn_script", "show_media"}
	cfg.BaseDir, cfg.ReadDirs, cfg.Workspace = sourceDir, []string{sourceDir}, "none"
	mgr, err := tools.NewToolManager(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.ApprovalMgr.Close()
	mgr.Registry.SetMediaPublisher(srv.mediaPublisher)

	const script = `const results = await Promise.all(Array.from({length: 9}, (_, i) => tools.show_media({path: i + '.png'})));
for (const [i, r] of results.entries()) {
  const url = r.text.match(/term-llm-media:\/\/[a-f0-9]{32}/)[0];
  text(i === 0 ? url.replace(/[a-f0-9]{32}$/, ref => ref.toUpperCase()) : url);
}`
	provider := nestedMediaReplyProvider{mockprovider.NewMockProvider("mock").AddToolCall(nestedMediaCallID, "spawn_script", map[string]any{
		"code": script, "max_wait": 30, "max_output_tokens": 1000,
	})}
	e := engine.NewEngine(provider, nil)
	defer e.CloseCodeMode()
	mgr.SetupEngine(e)
	rt := &serveRuntime{engine: e, provider: provider, store: store, defaultModel: sess.Model}
	run, err := srv.startResponseRun(rt, true, false, []core.Message{core.UserText("Show the figures.")},
		core.Request{SessionID: sess.ID, Tools: mgr.GetSpecs()}, sess.ID, startResponseRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.responseRuns.Close()
	select {
	case <-run.settled:
	case <-time.After(45 * time.Second):
		t.Fatal("nested media run did not settle")
	}
	requests := provider.RecordedRequests()
	if len(requests) != 2 || run.snapshot()["status"] != "completed" {
		t.Fatalf("expected a tool round and a completed reply, got %d requests; run: %v", len(requests), run.snapshot()["status"])
	}
	messages := requests[1].Messages
	output := nestedMediaToolText(responses.BuildResponsesContinuationInput(messages))
	refs := nestedMediaURL.FindAllStringSubmatch(output, -1)
	if len(refs) != len(figures) || strings.Contains(output, sourceDir) || strings.Contains(output, srv.mediaPublisher.dir) {
		t.Fatalf("expected nine URLs with no private paths in provider output: %s", output)
	}
	if full := nestedMediaToolText(responses.BuildResponsesInput(messages)); full != output {
		t.Fatal("full-history and continuation requests disagree on script output")
	}

	live := nestedMediaLiveEntries(t, run.events)
	if err := os.RemoveAll(sourceDir); err != nil { // Only the publisher's durable copies may serve the reply.
		t.Fatal(err)
	}
	cold := &serveServer{store: store, mediaPublisher: srv.mediaPublisher}
	reloaded, reply := nestedMediaReload(t, cold, sess.ID, output)
	if !slices.Equal(live, reloaded) || len(reloaded) != len(refs) {
		t.Fatalf("media mappings: live=%d, after reload=%d, model URLs=%d", len(live), len(reloaded), len(refs))
	}
	for k, ref := range refs {
		if !strings.Contains(reply, "![Figure]("+ref[0]+")") {
			t.Fatalf("final assistant Markdown lost %s on reload", ref[0])
		}
		i := slices.IndexFunc(reloaded, func(m webMediaEntry) bool { return strings.EqualFold(m.Reference, ref[1]) })
		if i < 0 {
			t.Fatalf("no published file for %s", ref[0])
		}
		rr := httptest.NewRecorder()
		cold.handleMedia(rr, httptest.NewRequest("GET", reloaded[i].URL, nil))
		if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rr.Body.Bytes(), figures[k]) {
			t.Fatalf("reference %s did not resolve to figure %d", ref[0], k)
		}
	}
}

func nestedMediaLiveEntries(t *testing.T, events []responseRunEvent) []webMediaEntry {
	t.Helper()
	var live []webMediaEntry
	for _, event := range events {
		if event.Event != "response.tool_exec.end" {
			continue
		}
		var end struct {
			CallID string          `json:"call_id"`
			Media  []webMediaEntry `json:"media"`
		}
		if err := json.Unmarshal(event.Data, &end); err != nil {
			t.Fatal(err)
		}
		if end.CallID == nestedMediaCallID {
			live = end.Media
		} else if len(end.Media) != 0 {
			t.Fatal("raw nested tool event published media")
		}
	}
	return live
}

func nestedMediaReload(t *testing.T, cold *serveServer, sessionID, output string) ([]webMediaEntry, string) {
	t.Helper()
	base := "/v1/sessions/" + sessionID
	rows := decodeInspect(t, inspectRequest(cold, "GET", base+"/inspect")).Blocks
	i := slices.IndexFunc(rows, func(b inspect.Block) bool {
		return b.Kind == "tool_result" && b.Tool != nil && b.Tool.CallID == nestedMediaCallID
	})
	if i < 0 {
		t.Fatal("missing script result in Inspect")
	}
	bodies := decodeBody[inspect.BodiesResponse](t, inspectRequest(cold, "GET", base+"/inspect/bodies?ids="+rows[i].ID))
	if len(bodies.Bodies) != 1 || !strings.Contains(bodies.Bodies[0].Text, output) {
		t.Error("Inspect hid or altered the model-facing output")
	}
	j := slices.IndexFunc(rows, func(b inspect.Block) bool { return b.Kind == "assistant_text" })
	if j < 0 {
		t.Fatal("missing final assistant response")
	}
	bodyIDs := rows[i].MessageID + "," + rows[j].MessageID
	transcript := decodeBody[transcriptBodiesResponse](t, inspectRequest(cold, "GET", base+"/transcript/bodies?ids="+bodyIDs))
	var reloaded []webMediaEntry
	var reply strings.Builder
	for _, msg := range transcript.Messages {
		for _, part := range msg.Parts {
			if part.Type == "tool_result" && part.ToolCallID == nestedMediaCallID {
				reloaded = part.Media
			}
			if msg.Role == "assistant" && part.Type == "text" {
				reply.WriteString(part.Text)
			}
		}
	}
	return reloaded, reply.String()
}
