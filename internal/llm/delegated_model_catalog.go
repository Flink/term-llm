package llm

import (
	"context"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	rp "github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
)

type delegatedCatalog struct {
	cached  func(string) ([]core.ModelInfo, bool, error)
	refresh func(context.Context, string) error
}

var delegatedCatalogs = map[config.ProviderType]delegatedCatalog{
	config.ProviderTypeGrok:    {rp.CachedDelegatedGrokModels, rp.RefreshDelegatedGrokModelsIfStale},
	config.ProviderTypeChatGPT: {rp.CachedDelegatedChatGPTModels, rp.RefreshDelegatedChatGPTModelsIfStale},
	config.ProviderTypeCopilot: {rp.CachedDelegatedCopilotModels, rp.RefreshDelegatedCopilotModelsIfStale},
}

func delegatedModelRoute(pc config.ProviderConfig) string {
	if pc.ProxyBinding != "" && pc.ProxyBinding == pc.BaseURL {
		return pc.BaseURL
	}
	return ""
}

func delegatedModelCatalogFor(provider string, pc config.ProviderConfig) (delegatedCatalog, bool) {
	discovery, ok := delegatedCatalogs[config.InferProviderType(provider, pc.Type)]
	return discovery, ok && delegatedModelRoute(pc) != ""
}

// RefreshDelegatedModelDiscovery primes completion without prompting for local
// OAuth. Explicit model lists override discovery, except for ChatGPT where the
// live catalog supplements configured entries.
func RefreshDelegatedModelDiscovery(ctx context.Context, cfg *config.Config, provider string) {
	if cfg == nil || !ProviderSelectable(cfg, provider) {
		return
	}
	pc := cfg.Providers[provider]
	if len(pc.Models) > 0 && config.InferProviderType(provider, pc.Type) != config.ProviderTypeChatGPT {
		return
	}
	if discovery, ok := delegatedModelCatalogFor(provider, pc); ok {
		_ = discovery.refresh(ctx, pc.BaseURL)
	}
}

// DelegatedModelCatalog returns the route's last known catalog without network
// access. The final bool distinguishes a cold delegated cache from direct auth.
func DelegatedModelCatalog(cfg *config.Config, provider string) (models []core.ModelInfo, fresh, delegated bool) {
	if cfg == nil {
		return nil, false, false
	}
	pc := cfg.Providers[provider]
	if discovery, ok := delegatedModelCatalogFor(provider, pc); ok {
		models, fresh, _ = discovery.cached(pc.BaseURL)
		return models, fresh, true
	}
	return nil, false, false
}

func delegatedModelIDs(models []core.ModelInfo, fallback string) []string {
	if len(models) == 0 && fallback != "" {
		return []string{fallback}
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// UsesDelegatedModelCatalog reports whether discovery is scoped to a proxy route.
func UsesDelegatedModelCatalog(cfg *config.Config, provider string) bool {
	if cfg == nil {
		return false
	}
	_, ok := delegatedModelCatalogFor(provider, cfg.Providers[provider])
	return ok
}
