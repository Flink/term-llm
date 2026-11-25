package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

func TestLiveReportAgyBin(t *testing.T) {
	stubAgyUsageConfig(t, &config.Config{})
	agyUsageLookPath = func(string) (string, error) { return "/bin/agy", nil }
	agyUsageCommand = func(context.Context, string) ([]byte, error) { return []byte(agyUsageFixture), nil }

	report, err := LiveReport(context.Background(), "agy-bin")
	if err != nil {
		t.Fatalf("LiveReport: %v", err)
	}
	if report.Provider != "antigravity" || report.Source != "Live from agy" || len(report.Limits) != 2 || report.Limits[0].Name != "Gemini Models" {
		t.Fatalf("report = %+v", report)
	}
}

func TestLiveReportAgyBinInvalidBindingNoFallback(t *testing.T) {
	stubAgyUsageConfig(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"agy-bin": {ProxyBinding: "https://unregistered.example.test/agy"},
	}})
	agyUsageLookPath = func(string) (string, error) {
		t.Fatal("invalid binding must not look up agy")
		return "", nil
	}
	if _, err := LiveReport(context.Background(), "agy-bin"); err == nil || !strings.Contains(err.Error(), "no direct fallback") {
		t.Fatalf("error = %v", err)
	}
}

// installFakeClaudeUsage answers the get_usage control request with response.
func installFakeClaudeUsage(t *testing.T, response string) {
	t.Helper()
	isolateClaudeUsage(t)
	installFakeClaude(t, `#!/bin/sh
read -r request
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"term-llm-usage","response":`+response+`}}'
`)
}

func TestLiveReportClaudeBin(t *testing.T) {
	installFakeClaudeUsage(t, `{"subscription_type":"max","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":25}}}`)
	report, err := LiveReport(context.Background(), "claude-bin")
	if err != nil {
		t.Fatalf("LiveReport: %v", err)
	}
	if report.Provider != "claude" || report.Plan != "max" || report.Source != "Live from Claude Code" || len(report.Limits) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if limit := report.Limits[0]; limit.Name != "Current session" || limit.PrimaryWindow == nil || limit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("limit = %+v", limit)
	}
}

func TestLiveReportClaudeBinExplainsMissingLimits(t *testing.T) {
	for _, test := range []struct {
		name, response, want string
	}{
		{"unavailable", `{"rate_limits_available":false}`, "user:profile"},
		{"no windows", `{"rate_limits_available":true,"rate_limits":{"five_hour":null}}`, "no active usage windows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			installFakeClaudeUsage(t, test.response)
			if _, err := LiveReport(context.Background(), "claude-bin"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLiveReportCursorBin(t *testing.T) {
	stubUsageConfig(t, &config.Config{})
	originalLookPath, originalCommand, originalClient := cursorUsageLookPath, cursorUsageCommand, cursorUsageClient
	t.Cleanup(func() {
		cursorUsageLookPath, cursorUsageCommand, cursorUsageClient = originalLookPath, originalCommand, originalClient
	})
	cursorUsageLookPath = func(string) (string, error) { return "/bin/agent", nil }
	cursorUsageCommand = func(context.Context, string) ([]byte, error) {
		return []byte(`{"auth":{"accessToken":"secret-token"}}`), nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("request = %s authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"planUsage":{"used":125,"limit":500,"remaining":375,"totalPercentUsed":25}}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("CURSOR_USAGE_BASE_URL", server.URL)
	cursorUsageClient = server.Client()

	report, err := LiveReport(context.Background(), "cursor-bin")
	if err != nil {
		t.Fatalf("LiveReport: %v", err)
	}
	if report.Provider != "cursor" || len(report.Limits) != 1 || report.Limits[0].PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("report = %+v", report)
	}
}

func TestLiveReportUsesConfiguredDirectEndpointAndKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nested/v1/usage" || r.Header.Get("Authorization") != "Bearer configured-key" {
			t.Errorf("request=%s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":42,"resetsAt":"2026-11-16T00:00:00Z"}}}`)
	}))
	t.Cleanup(server.Close)
	stubUsageConfig(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"opencode-go": {Type: config.ProviderTypeOpenCodeGo, BaseURL: server.URL + "/nested/v1/", ResolvedAPIKey: "configured-key"},
	}})
	report, err := LiveReport(context.Background(), "opencode-go")
	if err != nil || report.Provider != "opencode-go" || len(report.Limits) == 0 || report.Limits[0].PrimaryWindow.UsedPercent != 42 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestLiveReportRoutesOAuthDirectProviders(t *testing.T) {
	originalFetch := fetchDirectProviderUsage
	t.Cleanup(func() { fetchDirectProviderUsage = originalFetch })
	stubUsageConfig(t, &config.Config{})
	for _, provider := range []string{"chatgpt", "grok"} {
		fetchDirectProviderUsage = func(_ context.Context, got, apiKey string) (*providerusage.Report, error) {
			if got != provider || apiKey != "" {
				t.Fatalf("fetch args = provider %q, apiKey %q", got, apiKey)
			}
			return &providerusage.Report{Provider: provider}, nil
		}
		if report, err := LiveReport(context.Background(), provider); err != nil || report.Provider != provider {
			t.Fatalf("%s: report=%+v err=%v", provider, report, err)
		}
	}
}

func TestLiveReportRejectsUnsupportedProvider(t *testing.T) {
	if _, err := LiveReport(context.Background(), "openai"); err == nil || !strings.Contains(err.Error(), `live usage is not supported for provider "openai"`) {
		t.Fatalf("error = %v", err)
	}
}
