package usage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

// LiveReport fetches a provider's current subscription limits without starting
// a model turn. It shares each provider's fetcher with `term-llm usage`, so
// provider must be a canonical provider key such as "agy-bin" or "chatgpt".
func LiveReport(ctx context.Context, provider string) (*providerusage.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch provider {
	case "agy-bin":
		var report *providerusage.Report
		err := withAgyBinUsage(ctx, func(_ json.RawMessage, data agyBinUsageReport, source string) (err error) {
			if report, err = normalizeAgyBinUsage(data); err == nil {
				report.Source = source
			}
			return err
		})
		return report, err
	case "claude-bin":
		raw, source, err := loadClaudeBinUsage(ctx)
		if err != nil {
			return nil, err
		}
		report, err := decodeClaudeBinUsage(raw)
		if err != nil {
			return nil, err
		}
		return claudeBinProviderReport(report, source)
	case "cursor-bin":
		_, report, err := loadCursorBinUsage(ctx)
		if err != nil {
			return nil, err
		}
		return cursorBinProviderReport(report), nil
	case "chatgpt", "grok", "opencode-go":
		apiKey, err := resolvedProviderUsageAPIKey(provider)
		if err != nil {
			return nil, err
		}
		return fetchDirectProviderUsage(ctx, provider, apiKey)
	default:
		return nil, fmt.Errorf("live usage is not supported for provider %q", provider)
	}
}
