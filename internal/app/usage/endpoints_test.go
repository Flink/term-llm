package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
)

func TestDirectUsageConfiguredProviderRoot(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/nested/v1/usage" || r.Header.Get("Authorization") != "Bearer fake" {
			t.Errorf("request=%s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-16T00:00:00Z"},"weekly":{"status":"ok","resetsAt":"2026-11-16T00:00:00Z"},"monthly":{"status":"ok","resetsAt":"2026-11-16T00:00:00Z"}}}`)
	}))
	defer server.Close()
	old := loadProviderUsageConfig
	defer func() { loadProviderUsageConfig = old }()
	loadProviderUsageConfig = func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"opencode-go": {BaseURL: server.URL + "/nested/v1/"}}}, nil
	}
	report, err := fetchConfiguredDirectProviderUsage(context.Background(), "opencode-go", "fake")
	if err != nil || !called || report.Provider != "opencode-go" {
		t.Fatalf("report=%v called=%v err=%v", report, called, err)
	}
}

func TestGrokUsageRejectsImplicitLegacyRoot(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Error("OAuth usage must not contact an implicit legacy Grok root")
		http.Error(w, "unexpected request", http.StatusForbidden)
	}))
	defer server.Close()
	old := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = old })
	loadProviderUsageConfig = func() (*config.Config, error) {
		return &config.Config{Providers: map[string]config.ProviderConfig{"grok": {
			APIKey: "legacy-api-key", BaseURL: server.URL + "/v1",
		}}}, nil
	}
	_, err := fetchConfiguredDirectProviderUsage(context.Background(), "grok", "")
	if called || err == nil || !strings.Contains(err.Error(), "set explicit type: grok") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestClaudeProxyUsageDoesNotRelaxOtherProviderAllowance(t *testing.T) {
	old := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = old })
	loadProviderUsageConfig = func() (*config.Config, error) {
		return &config.Config{Proxy: config.ProxyConfig{URL: "https://proxy.example.test"}, Providers: map[string]config.ProviderConfig{
			"cursor-bin": {ProxyBinding: "https://proxy.example.test/cursor"},
		}}, nil
	}
	for _, provider := range []string{"cursor-bin"} {
		if err := refuseLocalProxyAllowance(provider); err == nil || !strings.Contains(err.Error(), "not available in this proxy mode") {
			t.Fatalf("%s allowance refusal = %v", provider, err)
		}
	}
}
