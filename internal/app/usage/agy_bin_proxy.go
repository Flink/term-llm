package usage

import (
	"errors"
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

// configuredAgyUsageProxy resolves only an explicitly configured agy-bin
// binding. A configured but invalid binding fails closed with no local
// Antigravity login fallback.
func configuredAgyUsageProxy() (*proxyclient.Binding, error) {
	cfg, err := loadProviderUsageConfig()
	if err != nil {
		return nil, errors.New("could not load Antigravity proxy usage configuration")
	}
	return configuredAgyUsageProxyFor(cfg, proxyclient.AgyNativeKind)
}

func configuredAgyUsageProxyFor(cfg *config.Config, providerName string) (*proxyclient.Binding, error) {
	if cfg == nil {
		return nil, nil
	}
	provider := cfg.GetProviderConfig(providerName)
	if provider == nil || strings.TrimSpace(provider.ProxyBinding) == "" {
		return nil, nil
	}
	binding, err := proxyclient.ResolveAgyNativeBinding(provider.ProxyBinding)
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// redactAgyProxyToken keeps the scoped token out of agy-provided response text.
func redactAgyProxyToken(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}
