package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestShellCompletionUsesDelegatedGrokCatalog(t *testing.T) {
	const token = "scoped-grok-completion-test-token-012345"
	var modelCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("request did not use proxy caller credentials")
		}
		switch r.URL.Path {
		case "/_proxy/client/v2/providers":
			fmt.Fprint(w, `{"revision":1,"providers":[{"id":"grok","family":"inference","routes":[{"name":"grok","profile":"grok","local_prefix":"/plan/"}]}]}`)
		case "/plan/models":
			modelCalls.Add(1)
			fmt.Fprint(w, `{"data":[{"model":"grok-live","api_backend":"responses","reasoning_efforts":[{"value":"high"}]},{"model":"grok-live-low","api_backend":"responses"},{"model":"grok-chat-only","api_backend":"chat"}]}`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "term-llm")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("proxy:\n  url: %s\n  token: %s\n  providers: [grok]\nproviders:\n  grok:\n    model: grok-live\n", server.URL, token)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	oldOut := rootCmd.OutOrStdout()
	rootCmd.SetOut(&out)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(oldOut) })
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"grok:", []string{"grok:grok-live", "grok:grok-live-low"}},
		{"grok:grok-live", []string{"grok:grok-live", "grok:grok-live-high", "grok:grok-live-low"}},
	} {
		out.Reset()
		if err := executeWithArgs([]string{cobra.ShellCompRequestCmd, "ask", "-p", tc.prefix}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if got := lines[:len(lines)-1]; !slices.Equal(got, tc.want) {
			t.Fatalf("completions for %q = %q, want %q", tc.prefix, got, tc.want)
		}
	}
	for _, key := range []string{"providers.grok.model", "providers.grok.fast_model"} {
		if got := configValueCompletions(key, "grok-"); !slices.Equal(got, []string{"grok-live", "grok-live-low"}) {
			t.Errorf("config completion for %s = %v", key, got)
		}
	}
	if calls := modelCalls.Load(); calls != 1 {
		t.Errorf("proxy model catalog requests = %d, want 1 (reuse warm cache)", calls)
	}
}
