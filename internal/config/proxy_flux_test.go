package config_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/image"
)

func TestProxyAllRevokedFluxNeverUsesLocalKeyOrEndpoint(t *testing.T) {
	const token = "proxy-flux-client-test-token"
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_proxy/client/v2/providers" {
			_, _ = w.Write([]byte(`{"providers":[{"id":"other","family":"inference","routes":[{"name":"other","local_prefix":"/flux/","profile":"openai-compatible"}]}]}`))
			return
		}
		attempts.Add(1)
		if r.URL.Path != "/_proxy/client/unavailable/flux/test-model" || r.Header.Get("x-key") != token {
			t.Error("revoked Flux attempted another endpoint or used local credentials")
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("BFL_API_KEY", "must-not-use-local-key")
	dir := filepath.Join(home, "term-llm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// If custody is bypassed the old configured vendor root also points at
	// the test server. The test never uses the Internet on a regression.
	body := "proxy:\n  url: " + srv.URL + "\n  token: " + token + "\nimage:\n  flux:\n    base_url: " + srv.URL + "/would-be-vendor\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Flux.BaseURL != srv.URL+"/_proxy/client/unavailable/flux" {
		t.Fatal("revoked Flux route did not fail closed")
	}
	p, err := image.NewImageProvider(cfg, "flux:test-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), image.GenerateRequest{Prompt: "hello"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("revoked Flux request: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("Flux request attempts = %d", attempts.Load())
	}
}
