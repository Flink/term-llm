package usage

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

func TestRunDirectProviderUsageWritesChatGPTPlanLimits(t *testing.T) {
	stubUsageConfig(t, &config.Config{})
	originalFetch := fetchProviderUsageWithBaseURL
	defer func() { fetchProviderUsageWithBaseURL = originalFetch }()
	fetchProviderUsageWithBaseURL = func(_ context.Context, provider, apiKey, baseURL string) (*providerusage.Report, error) {
		if provider != "chatgpt" {
			t.Fatalf("provider = %q", provider)
		}
		if apiKey != "" || baseURL != "" {
			t.Fatalf("apiKey = %q baseURL = %q, want empty for chatgpt", apiKey, baseURL)
		}
		return &providerusage.Report{
			Provider: "chatgpt",
			Plan:     "plus",
			Limits: []providerusage.Limit{{
				ID:   "codex",
				Name: "Codex",
				PrimaryWindow: &providerusage.Window{
					UsedPercent:     25,
					DurationMinutes: 300,
					ResetsAt:        time.Now().Add(time.Hour),
				},
			}},
		}, nil
	}

	o := NewOptions()
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	if err := o.runDirectProviderUsage(command, "chatgpt"); err != nil {
		t.Fatalf("runDirectProviderUsage: %v", err)
	}
	for _, want := range []string{"ChatGPT Codex", "Plus plan", "5 hour window\n  ██████░░░░░░░░░░░░░░░░  25% used"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunUsageRoutesGrokToOAuthUsageFetcher(t *testing.T) {
	stubUsageConfig(t, &config.Config{})
	originalFetch := fetchProviderUsageWithBaseURL
	defer func() { fetchProviderUsageWithBaseURL = originalFetch }()
	fetchProviderUsageWithBaseURL = func(_ context.Context, provider, apiKey, baseURL string) (*providerusage.Report, error) {
		if provider != "grok" || apiKey != "" || baseURL != "" {
			t.Fatalf("fetch args = provider %q, apiKey %q, baseURL %q", provider, apiKey, baseURL)
		}
		return &providerusage.Report{
			Provider: "grok",
			Limits: []providerusage.Limit{{
				Name: "Weekly limit",
				PrimaryWindow: &providerusage.Window{
					Label:       "1 week window",
					UsedPercent: 42,
				},
			}},
		}, nil
	}

	o := NewOptions()
	o.Provider = "grok"
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	if err := o.Run(command, nil); err != nil {
		t.Fatalf("runUsage: %v", err)
	}
	for _, want := range []string{"Grok", "Weekly limit", "1 week window", "42% used"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunUsageRejectsLocalFiltersForGrok(t *testing.T) {
	o := NewOptions()
	o.Provider = "grok"
	o.Since = "20260801"
	command := &cobra.Command{}
	if err := o.Run(command, nil); err == nil || !strings.Contains(err.Error(), "live grok usage does not support") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunDirectProviderUsageOpenCodeGoUsesConfiguredKey(t *testing.T) {
	originalFetch := fetchProviderUsageWithBaseURL
	originalLoad := loadProviderUsageConfig
	defer func() {
		fetchProviderUsageWithBaseURL = originalFetch
		loadProviderUsageConfig = originalLoad
	}()
	loads := 0
	loadProviderUsageConfig = func() (*config.Config, error) {
		loads++
		return &config.Config{Providers: map[string]config.ProviderConfig{
			"opencode-go": {Type: config.ProviderTypeOpenCodeGo, ResolvedAPIKey: "configured-key"},
		}}, nil
	}
	fetchProviderUsageWithBaseURL = func(_ context.Context, provider, apiKey, baseURL string) (*providerusage.Report, error) {
		if provider != "opencode-go" || apiKey != "configured-key" || baseURL != "" {
			t.Fatalf("fetch args = provider %q, apiKey %q, baseURL %q", provider, apiKey, baseURL)
		}
		return &providerusage.Report{
			Provider: "opencode-go",
			Limits: []providerusage.Limit{{
				Name: "Rolling usage",
				PrimaryWindow: &providerusage.Window{
					Label:       "5 hour window",
					UsedPercent: 42,
				},
			}},
		}, nil
	}

	o := NewOptions()
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	if err := o.runDirectProviderUsage(command, "opencode-go"); err != nil {
		t.Fatalf("runDirectProviderUsage: %v", err)
	}
	if loads != 1 {
		t.Fatalf("config loads = %d, want 1", loads)
	}
	for _, want := range []string{"OpenCode Go", "Rolling usage", "42% used"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}
