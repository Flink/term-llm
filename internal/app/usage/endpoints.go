package usage

import (
	"context"
	"fmt"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

var fetchProviderUsageWithBaseURL = providerusage.FetchWithBaseURL

func refuseLocalProxyAllowance(provider string) error {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return fmt.Errorf("load proxy usage configuration: %w", err)
	}
	return refuseLocalProxyAllowanceFor(cfg, provider)
}

func refuseLocalProxyAllowanceFor(cfg *config.Config, provider string) error {
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
	return fetchDirectProviderUsageWithConfig(ctx, cfg, provider, config.InferProviderType(provider, ""), apiKey)
}

func fetchDirectProviderUsageWithConfig(ctx context.Context, cfg *config.Config, providerKey string, providerType config.ProviderType, apiKey string) (*providerusage.Report, error) {
	base, err := resolvedDirectProviderUsageBaseURL(cfg, providerKey, providerType)
	if err != nil {
		return nil, err
	}
	return fetchProviderUsageWithBaseURL(ctx, string(providerType), apiKey, base)
}

func resolvedDirectProviderUsageBaseURL(cfg *config.Config, providerKey string, providerType config.ProviderType) (string, error) {
	if cfg == nil {
		return "", nil
	}
	pc := cfg.GetProviderConfig(providerKey)
	if err := config.ValidateProviderCompatibility(providerKey, pc); err != nil {
		return "", err
	}
	if pc == nil || config.InferProviderType(providerKey, pc.Type) != providerType {
		return "", nil
	}
	base, err := config.ResolveBaseURL("providers."+providerKey+".base_url", pc.BaseURL, "")
	if err != nil {
		return "", err
	}
	if providerType != config.ProviderTypeChatGPT && providerType != config.ProviderTypeGrok {
		return base, nil
	}
	if pc.ProxyBinding != "" {
		binding, ok := proxyclient.Lookup(base)
		if !ok || binding.Kind != string(providerType) || !proxyclient.SameRoot(base, binding.Root) || !proxyclient.SameRoot(pc.ProxyBinding, binding.Root) {
			return "", fmt.Errorf("provider %q has no matching registered proxy route for live usage (no direct fallback)", providerKey)
		}
	} else if base != "" && providerKey != string(providerType) {
		// A custom OAuth inference endpoint does not establish where to
		// fetch account usage or authorize use of the local OAuth session.
		return "", fmt.Errorf("provider %q needs a configured proxy binding for live usage with a custom OAuth endpoint (no direct fallback)", providerKey)
	}
	return base, nil
}
