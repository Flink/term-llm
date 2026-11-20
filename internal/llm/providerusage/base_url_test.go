package providerusage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/credentials"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

func TestChatGPTDedicatedUsageRoot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := credentials.SaveChatGPTCredentials(&credentials.ChatGPTCredentials{AccessToken: "fake", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/account/nested/wham/usage" || r.Header.Get("Authorization") != "Bearer fake" {
			t.Errorf("request=%s", r.URL)
		}
		fmt.Fprint(w, `{"plan_type":"pro"}`)
	}))
	defer server.Close()
	t.Setenv("CHATGPT_USAGE_BASE_URL", server.URL+"/account/nested/")
	t.Setenv("OPENAI_BASE_URL", "http://not-used.invalid")
	report, err := FetchWithBaseURL(context.Background(), "chatgpt", "", "http://codex-not-used.invalid")
	if err != nil || !called || report.Plan != "pro" {
		t.Fatalf("report=%v err=%v called=%v", report, err, called)
	}
}

func TestChatGPTUsageOnlyUsesItsGrantedCompanion(t *testing.T) {
	const token = "usage-test-scoped-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage/mounted/wham/usage" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected usage request: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"plan_type":"pro"}`)
	}))
	defer srv.Close()
	t.Setenv("CHATGPT_USAGE_BASE_URL", srv.URL+"/no-direct-fallback")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	binding := func(path string, related map[string]string) proxyclient.Binding {
		return proxyclient.Binding{Root: srv.URL + path, Kind: "chatgpt", Token: token, RelatedRoots: related}
	}
	if err := proxyclient.Register([]proxyclient.Binding{
		binding("/inference/codex", map[string]string{"chatgpt-backend": srv.URL + "/usage/mounted"}),
		binding("/usage/mounted", nil),
		binding("/work/codex", nil),
		binding("/work/chatgpt-backend", nil), // unrelated binding with a tempting literal path
	}); err != nil {
		t.Fatal(err)
	}
	report, err := FetchWithBaseURL(context.Background(), "chatgpt", "", srv.URL+"/inference/codex")
	if err != nil || report.Plan != "pro" {
		t.Fatalf("scoped usage report: %v (%v)", report, err)
	}
	if _, err := FetchWithBaseURL(context.Background(), "chatgpt", "", srv.URL+"/work/codex"); err == nil || !strings.Contains(err.Error(), "explicit chatgpt-backend proxy grant") {
		t.Fatalf("missing grant should fail before any request, got %v", err)
	}
}
