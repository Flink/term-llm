package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	modelsapp "github.com/term-llm/term-llm/internal/app/models"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm"
)

func TestShellCompletionListsProxyOpenCodeGoModels(t *testing.T) {
	const token = "scoped-completion-test-token-0123456789"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("missing scoped proxy credential on %s", r.URL.Path)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/_proxy/client/v2/providers":
			fmt.Fprint(w, `{"providers":[{"id":"opencode-go","family":"inference","routes":[{"name":"opencode-go","profile":"opencode-go","local_prefix":"/team/go/"}]}]}`)
		case "/team/go/models":
			requests.Add(1)
			if r.Header.Get("x-opencode-client") == "" {
				t.Error("missing OpenCode Go attribution on catalog request")
			}
			fmt.Fprint(w, `{"data":[{"id":"kimi-proxy-max"},{"id":"muse-proxy"}]}`)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "term-llm")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`default_provider: opencode-go
providers:
  opencode-go:
    model: kimi-proxy-max
proxy:
  url: %s
  token: %s
  providers: [opencode-go]
`, server.URL, token)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	oldOut, oldErr := rootCmd.OutOrStdout(), rootCmd.ErrOrStderr()
	var out, stderr bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&stderr)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
	})
	check := func(prefix, want string) {
		t.Helper()
		out.Reset()
		if err := executeWithArgs([]string{cobra.ShellCompRequestCmd, "ask", "-p", prefix}); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Errorf("completion for %q = %q, want %q", prefix, got, want)
		}
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	check("opencode-go:", "opencode-go:kimi-proxy-max\nopencode-go:muse-proxy\n:4")
	check("opencode-go:ki", "opencode-go:kimi-proxy-max\n:4")
	if got := requests.Load(); got != 1 {
		t.Errorf("cold and warm completion made %d model requests, want 1", got)
	}

	// A separate cache proves the models command itself populates completion.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	options := modelsapp.Options{Provider: "opencode-go", JSON: true, LoadConfig: loadConfigWithSetup}
	if err := options.Run(modelsCmd, nil); err != nil {
		t.Fatalf("listing proxy models: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Check the cache-only path so a live completion refresh cannot mask
	// a missing cache write by the models command.
	if got := llm.GetProviderCompletions("opencode-go:mu", false, cfg); len(got) != 1 || got[0] != "opencode-go:muse-proxy" {
		t.Errorf("completions cached by models command = %v, want [opencode-go:muse-proxy]", got)
	}
}
