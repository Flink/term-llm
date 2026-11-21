package clibin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestAgyBoundStreamAndModelsFailBeforeAnySubprocess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires a supported native worker platform")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TERM_LLM_CLI_WIRE_TRACE", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	for _, test := range []struct{ name, kind, needle string }{
		{"unregistered", "", "registered scoped"},
		{"wrong-kind", "claude-bin", "registered scoped"},
		{"bootstrap-denied", "agy-bin", "HTTP 403"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := srv.URL + "/" + test.name
			if test.kind != "" {
				if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: test.kind, Token: "sk-tlp-fixture"}}); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(t.TempDir(), "executed")
			binary := writeAgyTestScript(t, "touch '"+marker+"'\nexit 99\n")
			t.Setenv("PATH", filepath.Dir(binary)+":"+os.Getenv("PATH"))
			p := NewAgyBinProvider("", map[string]string{agyProxyRootEnv: root})
			p.realHome = t.TempDir()
			authDir := filepath.Join(p.realHome, ".gemini", "antigravity-cli")
			if err := os.MkdirAll(authDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(authDir, "antigravity-oauth-token"), []byte("local-credential-canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			stream, err := p.Stream(context.Background(), core.Request{Ephemeral: true})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			event, err := stream.Recv()
			if err != nil || event.Type != core.EventError || event.Err == nil || !strings.Contains(event.Err.Error(), test.needle) {
				t.Fatalf("stream failed for the wrong reason: type=%v event error=%v recv error=%v", event.Type, event.Err, err)
			}
			if _, err := p.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("models failed for the wrong reason: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("a bound worker still spawned agy")
			}
		})
	}
}
