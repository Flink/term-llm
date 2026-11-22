package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

func TestDelegatedOAuthModelCompletion(t *testing.T) {
	const token = "scoped-llm-completion-test-token-012345"
	var chatGPTCalls, copilotCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("request did not use proxy caller credentials")
		}
		switch r.URL.Path {
		case "/_proxy/client/v2/providers":
			fmt.Fprint(w, `{"revision":1,"providers":[{"id":"chatgpt","family":"inference","routes":[{"name":"chatgpt","profile":"chatgpt","local_prefix":"/codex/"}]},{"id":"copilot","family":"inference","routes":[{"name":"copilot","profile":"copilot","local_prefix":"/github/"}]}]}`)
		case "/codex/models":
			chatGPTCalls.Add(1)
			fmt.Fprint(w, `{"models":[{"slug":"proxy-codex","supported_reasoning_levels":["high"],"additional_speed_tiers":["fast"]}]}`)
		case "/github/models":
			copilotCalls.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"proxy-copilot","name":"Proxy Copilot"}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "term-llm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("proxy:\n  url: %s\n  token: %s\n  providers: [chatgpt, copilot]\nproviders:\n  chatgpt:\n    model: pinned-codex\n    models: [pinned-codex]\n", server.URL, token)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"chatgpt:", []string{"chatgpt:pinned-codex", "chatgpt:proxy-codex"}},
		{"chatgpt:proxy-codex", []string{"chatgpt:proxy-codex", "chatgpt:proxy-codex-fast", "chatgpt:proxy-codex-high"}},
		{"chatgpt:proxy-codex-high", []string{"chatgpt:proxy-codex-high", "chatgpt:proxy-codex-high-fast"}},
		{"copilot:", []string{"copilot:proxy-copilot"}},
	} {
		got, _ := ProviderFlagCompletion(nil, nil, tc.prefix)
		if !slices.Equal(got, tc.want) {
			t.Errorf("completion for %q = %v, want %v", tc.prefix, got, tc.want)
		}
	}
	if got := configValueCompletions("providers.copilot.fast_model", ""); !slices.Equal(got, []string{"proxy-copilot"}) {
		t.Errorf("Copilot fast_model completion = %v", got)
	}
	if chatGPTCalls.Load() != 1 || copilotCalls.Load() != 1 {
		t.Errorf("catalog requests: ChatGPT %d, Copilot %d, want one each", chatGPTCalls.Load(), copilotCalls.Load())
	}
}
