package usage

import (
	"context"
	"fmt"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

func refuseLocalProxyAllowance(provider string) error {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return fmt.Errorf("load proxy usage configuration: %w", err)
	}
	if cfg != nil {
		if pc := cfg.GetProviderConfig(provider); pc != nil && pc.ProxyBinding != "" {
			dashboard := "the credential proxy dashboard"
			if cfg.Proxy.URL != "" {
				dashboard = cfg.Proxy.URL + "/_proxy/"
			}
			return fmt.Errorf("%s vendor allowance is not available in this proxy mode; open %s for measured activity (allowance and possible plan overage remain unknown)", provider, dashboard)
		}
	}
	return nil
}

func fetchConfiguredDirectProviderUsage(ctx context.Context, provider, apiKey string) (*providerusage.Report, error) {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return nil, fmt.Errorf("load endpoint for %s usage: %w", provider, err)
	}
	base := ""
	if cfg != nil {
		if err := config.ValidateProviderCompatibility(provider, cfg.GetProviderConfig(provider)); err != nil {
			return nil, err
		}
		base, err = cfg.ProviderBaseURL(provider, "", "")
		if err != nil {
			return nil, err
		}
	}
	return providerusage.FetchWithBaseURL(ctx, provider, apiKey, base)
}
