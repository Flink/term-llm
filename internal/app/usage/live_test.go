package usage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
	"github.com/term-llm/term-llm/internal/proxyclient"
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
	old := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = old })
	loads := 0
	loadProviderUsageConfig = func() (*config.Config, error) {
		loads++
		return &config.Config{Providers: map[string]config.ProviderConfig{
			"opencode-go": {Type: config.ProviderTypeOpenCodeGo, BaseURL: server.URL + "/nested/v1/", ResolvedAPIKey: "configured-key"},
		}}, nil
	}
	report, err := LiveReport(context.Background(), "opencode-go")
	if err != nil || report.Provider != "opencode-go" || len(report.Limits) == 0 || report.Limits[0].PrimaryWindow.UsedPercent != 42 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if loads != 1 {
		t.Fatalf("config loads = %d, want 1", loads)
	}
}

func TestLiveReportRoutesOAuthDirectProviders(t *testing.T) {
	originalFetch := fetchProviderUsageWithBaseURL
	t.Cleanup(func() { fetchProviderUsageWithBaseURL = originalFetch })
	stubUsageConfig(t, &config.Config{})
	for _, provider := range []string{"chatgpt", "grok"} {
		fetchProviderUsageWithBaseURL = func(_ context.Context, got, apiKey, baseURL string) (*providerusage.Report, error) {
			if got != provider || apiKey != "" || baseURL != "" {
				t.Fatalf("fetch args = provider %q, apiKey %q, baseURL %q", got, apiKey, baseURL)
			}
			return &providerusage.Report{Provider: provider}, nil
		}
		if report, err := LiveReport(context.Background(), provider); err != nil || report.Provider != provider {
			t.Fatalf("%s: report=%+v err=%v", provider, report, err)
		}
	}
}

func TestLiveReportRejectsUnsupportedProvider(t *testing.T) {
	stubUsageConfig(t, &config.Config{})
	if _, err := LiveReport(context.Background(), "openai"); err == nil || !strings.Contains(err.Error(), `live usage is not supported for provider "openai"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLiveReportCanonicalizesProviderName(t *testing.T) {
	stubAgyUsageConfig(t, &config.Config{})
	agyUsageLookPath = func(string) (string, error) { return "/bin/agy", nil }
	agyUsageCommand = func(context.Context, string) ([]byte, error) { return []byte(agyUsageFixture), nil }

	report, err := LiveReport(context.Background(), "  AGY-BIN \t")
	if err != nil {
		t.Fatalf("LiveReport: %v", err)
	}
	if report.Provider != "antigravity" || len(report.Limits) != 2 {
		t.Fatalf("report = %+v", report)
	}

	originalFetch := fetchProviderUsageWithBaseURL
	t.Cleanup(func() { fetchProviderUsageWithBaseURL = originalFetch })
	fetchProviderUsageWithBaseURL = func(_ context.Context, got, apiKey, baseURL string) (*providerusage.Report, error) {
		if got != "chatgpt" || apiKey != "" || baseURL != "" {
			t.Fatalf("fetch args = provider %q, apiKey %q, baseURL %q", got, apiKey, baseURL)
		}
		return &providerusage.Report{Provider: "chatgpt"}, nil
	}
	if report, err := LiveReport(context.Background(), " ChatGPT "); err != nil || report.Provider != "chatgpt" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestLiveReportSanitizesConfigLoadErrors(t *testing.T) {
	old := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = old })
	loadProviderUsageConfig = func() (*config.Config, error) {
		return nil, errors.New("secret-config-body")
	}
	_, err := LiveReport(context.Background(), "claude-bin")
	if err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("error = %v", err)
	}
}

func TestLiveReportWithConfigSupportsCustomAliases(t *testing.T) {
	t.Run("opencode-go alias", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/household/v1/usage" || r.Header.Get("Authorization") != "Bearer household-key" {
				t.Errorf("request=%s auth=%q", r.URL, r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":19,"resetsAt":"2026-11-16T00:00:00Z"}}}`)
		}))
		t.Cleanup(server.Close)

		cfg := &config.Config{Providers: map[string]config.ProviderConfig{
			"household": {Type: config.ProviderTypeOpenCodeGo, BaseURL: server.URL + "/household/v1/", APIKey: "household-key"},
		}}
		report, err := LiveReportWithConfig(context.Background(), cfg, "household")
		if err != nil || report.Provider != "opencode-go" || len(report.Limits) == 0 || report.Limits[0].PrimaryWindow.UsedPercent != 19 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})

	t.Run("chatgpt alias uses its registered proxy root", func(t *testing.T) {
		const root = "https://proxy.example.test/work-chatgpt"
		if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: "chatgpt", Token: "test-scoped-client-token"}}); err != nil {
			t.Fatal(err)
		}
		originalFetch := fetchProviderUsageWithBaseURL
		t.Cleanup(func() { fetchProviderUsageWithBaseURL = originalFetch })
		fetchProviderUsageWithBaseURL = func(_ context.Context, got, apiKey, baseURL string) (*providerusage.Report, error) {
			if got != "chatgpt" || apiKey != "" || baseURL != "https://proxy.example.test/work-chatgpt" {
				t.Fatalf("fetch args = provider %q, apiKey %q, baseURL %q", got, apiKey, baseURL)
			}
			return &providerusage.Report{Provider: "chatgpt"}, nil
		}
		cfg := &config.Config{Providers: map[string]config.ProviderConfig{
			"work-chatgpt": {Type: config.ProviderTypeChatGPT, BaseURL: root + "/", ProxyBinding: root},
		}}
		if report, err := LiveReportWithConfig(context.Background(), cfg, "work-chatgpt"); err != nil || report.Provider != "chatgpt" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})

	for _, providerType := range []config.ProviderType{config.ProviderTypeAgyBin, config.ProviderTypeClaudeBin, config.ProviderTypeCursorBin} {
		t.Run(string(providerType)+" does not fall back to the default account", func(t *testing.T) {
			cfg := &config.Config{Providers: map[string]config.ProviderConfig{
				"work": {Type: providerType, Env: map[string]string{"HOME": t.TempDir()}},
			}}
			if _, err := LiveReportWithConfig(context.Background(), cfg, "work"); err == nil || !strings.Contains(err.Error(), "per-provider CLI environment") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	t.Run("agy-bin alias enforces proxy binding fail closed", func(t *testing.T) {
		oldLook := agyUsageLookPath
		t.Cleanup(func() { agyUsageLookPath = oldLook })
		agyUsageLookPath = func(string) (string, error) {
			t.Fatal("invalid alias binding must not look up agy")
			return "", nil
		}
		cfg := &config.Config{Providers: map[string]config.ProviderConfig{
			"work-agy": {Type: config.ProviderTypeAgyBin, ProxyBinding: "https://unregistered.example.test/work-agy"},
		}}
		if _, err := LiveReportWithConfig(context.Background(), cfg, "work-agy"); err == nil || !strings.Contains(err.Error(), "no direct fallback") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("claude-bin alias enforces proxy binding fail closed", func(t *testing.T) {
		cfg := &config.Config{Providers: map[string]config.ProviderConfig{
			"work-claude": {Type: config.ProviderTypeClaudeBin, ProxyBinding: "https://unregistered.example.test/work-claude"},
		}}
		if _, err := LiveReportWithConfig(context.Background(), cfg, "work-claude"); err == nil || !strings.Contains(err.Error(), "no direct fallback") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("cursor-bin alias refuses local proxy allowance", func(t *testing.T) {
		cfg := &config.Config{
			Proxy: config.ProxyConfig{URL: "https://proxy.example.test"},
			Providers: map[string]config.ProviderConfig{
				"work-cursor": {Type: config.ProviderTypeCursorBin, ProxyBinding: "https://proxy.example.test/work-cursor"},
			},
		}
		if _, err := LiveReportWithConfig(context.Background(), cfg, "work-cursor"); err == nil || !strings.Contains(err.Error(), "not available in this proxy mode") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("overridden built-in name to non-live type is rejected", func(t *testing.T) {
		cfg := &config.Config{Providers: map[string]config.ProviderConfig{
			"claude-bin": {Type: config.ProviderTypeAnthropic},
		}}
		if _, err := LiveReportWithConfig(context.Background(), cfg, "claude-bin"); err == nil || !strings.Contains(err.Error(), `live usage is not supported for provider "claude-bin"`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLiveOAuthUsageDoesNotFallBackToLocalCredentials(t *testing.T) {
	originalFetch := fetchProviderUsageWithBaseURL
	t.Cleanup(func() { fetchProviderUsageWithBaseURL = originalFetch })
	fetchProviderUsageWithBaseURL = func(context.Context, string, string, string) (*providerusage.Report, error) {
		t.Error("unsafe OAuth route reached the usage fetcher")
		return nil, nil
	}
	for _, kind := range []config.ProviderType{config.ProviderTypeChatGPT, config.ProviderTypeGrok} {
		const root = "https://unregistered.example.test/oauth-usage"
		for name, pc := range map[string]config.ProviderConfig{
			"custom endpoint":      {Type: kind, BaseURL: root},
			"unregistered binding": {Type: kind, BaseURL: root, ProxyBinding: root},
			"missing endpoint":     {Type: kind, ProxyBinding: root},
		} {
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				cfg := &config.Config{Providers: map[string]config.ProviderConfig{"work": pc}}
				if _, err := LiveReportWithConfig(context.Background(), cfg, "work"); err == nil || !strings.Contains(err.Error(), "no direct fallback") {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}

func TestNewLiveFetcherReusesConfigSnapshotWithoutReloadingOrRacing(t *testing.T) {
	oldLoad := loadProviderUsageConfig
	t.Cleanup(func() { loadProviderUsageConfig = oldLoad })
	loadProviderUsageConfig = func() (*config.Config, error) {
		t.Fatal("NewLiveFetcher must not reload config from disk")
		return nil, nil
	}

	originalFetch := fetchProviderUsageWithBaseURL
	t.Cleanup(func() { fetchProviderUsageWithBaseURL = originalFetch })
	fetchProviderUsageWithBaseURL = func(_ context.Context, provider, apiKey, baseURL string) (*providerusage.Report, error) {
		if provider != "opencode-go" || apiKey != "snapshot-key" || baseURL != "https://proxy.example.test/opencode-go" {
			return nil, fmt.Errorf("unexpected fetch args: provider=%q apiKey=%q baseURL=%q", provider, apiKey, baseURL)
		}
		return &providerusage.Report{Provider: "opencode-go"}, nil
	}

	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"opencode-go": {
			Type:    config.ProviderTypeOpenCodeGo,
			BaseURL: "https://proxy.example.test/opencode-go/",
			APIKey:  "snapshot-key",
			Env:     map[string]string{"FOO": "bar"},
		},
	}}
	fetch := NewLiveFetcher(cfg)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			cfg.Providers["opencode-go"] = config.ProviderConfig{
				Type:   config.ProviderTypeOpenCodeGo,
				APIKey: "mutated-key",
			}
			cfg.Providers["other"] = config.ProviderConfig{Type: config.ProviderTypeOpenAI}
		}
	}()
	for range 2 {
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				report, err := fetch(context.Background(), "opencode-go")
				if err != nil || report.Provider != "opencode-go" {
					t.Errorf("fetch: report=%+v err=%v", report, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
