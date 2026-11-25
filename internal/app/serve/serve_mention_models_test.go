package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
	"github.com/term-llm/term-llm/internal/mentions"
)

func TestMentionSearchAuthenticatedModelOnlyUsesFullUTF16Span(t *testing.T) {
	t.Setenv("TERM_LLM_AT_MENTIONS", "1")
	s := &serveServer{
		cfg:             serveServerConfig{requireAuth: true, token: "test-token"},
		cfgRef:          mentionTestConfig("claude-bin"),
		projectsEnabled: true,
		worktreeRootFn:  func() (string, error) { t.Fatal("model search attempted file lookup"); return "", nil },
		modelsProviders: map[string]core.Provider{"claude-bin": mockprovider.NewMockProvider("claude-bin")},
	}
	s.cfgRef.Providers["claude-bin"] = config.ProviderConfig{Model: "opus"}
	s.setCachedModelsForProvider("claude-bin", []core.ModelInfo{{ID: "opus", ReasoningEfforts: []string{"medium"}, DefaultReasoningEffort: "medium"}})
	text := "😀 pick @m:opustail now"
	request := serveMentionSearchRequest{Text: text, CursorUTF16: utf16Len("😀 pick @m:opus"), NoProject: true, WorktreeDir: "/irrelevant"}
	handler := s.httpHandler()
	if rr := postMentionSearch(t, handler, request, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d: %s", rr.Code, rr.Body)
	}
	got := mentionResponse(t, postMentionSearch(t, handler, request, "test-token"))
	wantToken := serveMentionToken{StartUTF16: utf16Len("😀 pick "), EndUTF16: utf16Len("😀 pick @m:opustail"), Query: "opus"}
	if !got.Active || !got.ModelOnly || got.Token != nil || len(got.Items) != 0 || got.ModelToken == nil || *got.ModelToken != wantToken {
		t.Fatalf("wrong scoped response: %+v, model token %+v", got, got.ModelToken)
	}
	if len(got.ModelItems) != 1 || got.ModelItems[0].InsertText != "@model:claude-bin:opus#" || !got.ModelItems[0].Continues || got.ModelItems[0].Badge != "latest" {
		t.Fatalf("model rows = %+v", got.ModelItems)
	}
	got = mentionResponse(t, postMentionSearch(t, handler, mentionTestRequest("@model:CLAUDE-BIN:OPUS#med"), "test-token"))
	if len(got.ModelItems) != 1 || got.ModelItems[0].InsertText != "claude-bin:opus-medium" || got.ModelItems[0].Continues || got.ModelItems[0].Badge != "default" {
		t.Fatalf("effort rows = %+v", got.ModelItems)
	}
	t.Setenv("TERM_LLM_AT_MENTIONS", "0")
	got = mentionResponse(t, postMentionSearch(t, handler, mentionTestRequest("@m:opus"), "test-token"))
	if got.Active || got.ModelOnly || len(got.ModelItems) != 0 {
		t.Fatalf("model autocomplete not disabled: %+v", got)
	}
	got = mentionResponse(t, postMentionSearch(t, handler, mentionTestRequest("@unrelated"), "test-token"))
	if got.Active || got.Token != nil || len(got.Items) != 0 {
		t.Fatalf("file autocomplete not disabled: %+v", got)
	}
}

func TestMentionSearchLooseModelsAreAdditiveAndSurviveFileFailure(t *testing.T) {
	t.Setenv("TERM_LLM_AT_MENTIONS", "1")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "corp-notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		rootFn func() (string, error)
		build  func(context.Context, string, mentions.BuildOptions) (*mentions.Snapshot, error)
	}{
		{name: "file match", rootFn: func() (string, error) { return root, nil }},
		{name: "root failure", rootFn: func() (string, error) { return "", errors.New("unavailable") }},
		{name: "index failure", rootFn: func() (string, error) { return root, nil }, build: func(context.Context, string, mentions.BuildOptions) (*mentions.Snapshot, error) {
			return nil, errors.New("unavailable")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &serveServer{cfgRef: mentionTestConfig("corp"), worktreeRootFn: test.rootFn, mentionBuildFn: test.build}
			s.setCachedModelsForProvider("corp", []core.ModelInfo{{ID: "fast"}})
			handler := http.HandlerFunc(s.handleMentionSearch)
			req := mentionTestRequest("@corporation now")
			req.CursorUTF16 = utf16Len("@corp")
			got := mentionResponse(t, postMentionSearch(t, handler, req, ""))
			if !got.Active || got.ModelOnly || got.ModelToken == nil || got.ModelToken.EndUTF16 != utf16Len("@corporation") || len(got.ModelItems) != 1 || got.ModelItems[0].InsertText != "corp:fast" {
				t.Fatalf("loose response = %+v, model token %+v", got, got.ModelToken)
			}
			if test.name != "file match" {
				if got.Token != nil || len(got.Items) != 0 {
					t.Fatalf("file rows after error = %+v", got)
				}
				return
			}
			if got.Token == nil || got.Token.EndUTF16 != utf16Len("@corp") || len(got.Items) != 1 || got.Items[0].Path != "corp-notes.txt" {
				t.Fatalf("file matches = %+v, token %+v", got.Items, got.Token)
			}
		})
	}
}

func TestMentionSearchModelDiscoveryIsProgressiveAndDeduplicated(t *testing.T) {
	slowStarted := make(chan struct{})
	allowFailure := make(chan struct{})
	fastStarted := make(chan struct{})
	var slowCalls atomic.Int32
	s := &serveServer{cfgRef: mentionTestConfig("fast", "slow"), shutdownCh: make(chan struct{})}
	t.Cleanup(func() { close(s.shutdownCh) })
	s.modelsProviders = map[string]core.Provider{
		"fast": &mentionModelsProvider{Provider: mockprovider.NewMockProvider("fast"), list: func(context.Context) ([]core.ModelInfo, error) {
			close(fastStarted)
			return []core.ModelInfo{{ID: "remote-model"}}, nil
		}},
		"slow": &mentionModelsProvider{Provider: mockprovider.NewMockProvider("slow"), list: func(ctx context.Context) ([]core.ModelInfo, error) {
			if slowCalls.Add(1) == 1 {
				close(slowStarted)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-allowFailure:
				return nil, errors.New("provider unavailable")
			}
		}},
	}
	handler := http.HandlerFunc(s.handleMentionSearch)
	req := mentionTestRequest("@m:remote-model")
	got := mentionResponse(t, postMentionSearch(t, handler, req, ""))
	if !got.ModelsPending {
		t.Fatalf("cold search should report pending: %+v", got)
	}
	waitMentionSignal(t, slowStarted)
	waitMentionSignal(t, fastStarted)
	waitMentionCondition(t, func() bool {
		got = mentionResponse(t, postMentionSearch(t, handler, req, ""))
		return len(got.ModelItems) == 1
	})
	if !got.ModelsPending || got.ModelItems[0].InsertText != "fast:remote-model" || slowCalls.Load() != 1 {
		t.Fatalf("fast rows must not wait for slow provider: %+v; slow calls %d", got, slowCalls.Load())
	}
	close(allowFailure)
	waitMentionCondition(t, func() bool {
		got = mentionResponse(t, postMentionSearch(t, handler, req, ""))
		return !got.ModelsPending
	})
	if len(got.ModelItems) != 1 || got.ModelItems[0].InsertText != "fast:remote-model" || slowCalls.Load() != 1 {
		t.Fatalf("failure lost results or immediately retried: %+v; slow calls %d", got, slowCalls.Load())
	}
}

func TestMentionSearchModelRefreshCancelsOnShutdown(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	s := &serveServer{cfgRef: mentionTestConfig("corp"), shutdownCh: make(chan struct{})}
	s.modelsProviders = map[string]core.Provider{"corp": &mentionModelsProvider{Provider: mockprovider.NewMockProvider("corp"), list: func(ctx context.Context) ([]core.ModelInfo, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return []core.ModelInfo{{ID: "late-result"}}, nil
	}}}
	s.mentionModelOptions("corp", true, time.Now())
	waitMentionSignal(t, started)
	close(s.shutdownCh)
	waitMentionSignal(t, cancelled)
	waitMentionCondition(t, func() bool {
		_, pending := s.mentionModelOptions("corp", true, time.Now())
		return !pending
	})
	// A lister can return concurrently with shutdown before its context
	// observes cancellation. Neither shared cache may be repopulated.
	s.setCachedModelsForProvider("corp", []core.ModelInfo{{ID: "late-cache-write"}})
	if got := s.getCachedModelsForProvider("corp"); len(got) != 0 {
		t.Fatalf("model listing cached a late response at shutdown: %+v", got)
	}
	if _, _, err := s.getModelsProvider("new-provider"); !errors.Is(err, context.Canceled) {
		t.Fatalf("late provider creation = %v, want context cancellation", err)
	}
}

func mentionTestConfig(ids ...string) *config.Config {
	cfg := &config.Config{ProviderDiscovery: config.ProviderDiscoveryConfig, Providers: make(map[string]config.ProviderConfig)}
	for _, id := range ids {
		cfg.Providers[id] = config.ProviderConfig{Type: config.ProviderTypeOpenAICompat}
	}
	return cfg
}

type mentionModelsProvider struct {
	core.Provider
	list func(context.Context) ([]core.ModelInfo, error)
}

func (p *mentionModelsProvider) ListModels(ctx context.Context) ([]core.ModelInfo, error) {
	return p.list(ctx)
}

func mentionTestRequest(text string) serveMentionSearchRequest {
	return serveMentionSearchRequest{Text: text, CursorUTF16: utf16Len(text)}
}

func postMentionSearch(t *testing.T, handler http.Handler, req serveMentionSearchRequest, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/mentions/search", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	return rr
}

func mentionResponse(t *testing.T, rr *httptest.ResponseRecorder) serveMentionSearchResponse {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	var got serveMentionSearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func waitMentionSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for model listing")
	}
}

func waitMentionCondition(t *testing.T, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for !check() {
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("timed out waiting for model refresh")
		}
	}
}
