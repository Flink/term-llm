package llm

import (
	"github.com/term-llm/term-llm/internal/config"
)

// ProviderInfo is a configured provider and its pickable model IDs.
type ProviderInfo struct {
	Name   string
	Models []string
}

// AvailableProviders returns configured providers with their models in
// consistent order: the same set the web UI picker and `term-llm providers`
// treat as configured under provider_discovery (see
// config.ProviderConfiguredVia). hasLocalCredentials reports built-in login
// state; callers pass the same cache their pickers use.
func AvailableProviders(cfg *config.Config, hasLocalCredentials func(string) bool) []ProviderInfo {
	var providers []ProviderInfo
	seen := make(map[string]bool)

	// Add built-in providers first, merging any configured model
	for _, name := range config.GetBuiltInProviderNames() {
		seen[name] = true
		if cfg.ProviderConfiguredVia(name, hasLocalCredentials) == "" {
			continue
		}
		curated := ModelIDsForConfiguredProvider(cfg, name)
		curated = ProviderModelIDsForDiscovery(cfg, name, curated)
		if len(curated) == 0 {
			continue
		}
		var defaultModel string
		if cfg != nil {
			if providerCfg, ok := cfg.Providers[name]; ok {
				defaultModel = providerCfg.Model
			}
		}
		ordered := SortModelIDsByPopularity(name, defaultModel, curated)
		providers = append(providers, ProviderInfo{
			Name:   name,
			Models: ExpandProviderModelIDsForDiscovery(cfg, name, ordered),
		})
	}

	// Add custom configured providers from config
	if cfg != nil {
		for name, providerCfg := range cfg.Providers {
			if seen[name] || cfg.ProviderConfiguredVia(name, nil) == "" {
				continue
			}
			source := providerCfg.Models
			if UsesDelegatedModelCatalog(cfg, name) || len(source) == 0 {
				source = ModelIDsForConfiguredProvider(cfg, name)
			}
			source = ProviderModelIDsForDiscovery(cfg, name, source)
			ordered := SortModelIDsByPopularity(name, providerCfg.Model, source)
			if len(ordered) == 0 {
				continue
			}
			providers = append(providers, ProviderInfo{
				Name:   name,
				Models: ExpandProviderModelIDsForDiscovery(cfg, name, ordered),
			})
		}
	}

	return providers
}
