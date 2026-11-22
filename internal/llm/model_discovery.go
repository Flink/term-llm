package llm

import (
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
)

// ModelIDsForConfiguredProvider gives offline pickers the route-specific list
// rather than unscoped type defaults for delegated OAuth providers.
func ModelIDsForConfiguredProvider(cfg *config.Config, provider string) []string {
	if catalog, _, delegated := DelegatedModelCatalog(cfg, provider); delegated {
		if explicit, ok := configuredCompletionModels(cfg, provider, false, cfg.Providers[provider]); ok {
			return explicit
		}
		return delegatedModelIDs(catalog, cfg.Providers[provider].Model)
	}
	return ResolveProviderModelIDs(provider)
}

func discoveryEffortSplitter(cfg *config.Config, provider string) func(string) (string, string) {
	catalog, _, _ := DelegatedModelCatalog(cfg, provider)
	return func(model string) (string, string) {
		if base, effort, _, ok := responsesproviders.SplitCatalogModelEffort(catalog, model); ok {
			return base, effort
		}
		return BaseModelAndEffortForProvider(provider, model)
	}
}

func cachedCompletionCandidates(provider string, isImage bool, pc config.ProviderConfig, cfg *config.Config, getModelIDs func(string) []string) []string {
	if !isImage {
		if models, _, delegated := DelegatedModelCatalog(cfg, provider); delegated {
			return delegatedModelIDs(models, pc.Model)
		}
	}
	ids, _ := completionModelCandidates(provider, isImage, pc, cfg, getModelIDs)
	return ids
}

func expandModelIDsForDiscovery(cfg *config.Config, provider string, ids []string) []string {
	catalog, _, delegated := DelegatedModelCatalog(cfg, provider)
	if !delegated || len(catalog) == 0 {
		return ExpandWithEffortVariantsForProvider(provider, ids)
	}
	return expandEffortVariants(ids, func(id string) []string {
		base, effort, efforts, known := responsesproviders.SplitCatalogModelEffort(catalog, id)
		if !known {
			return providerBaseEfforts(provider, id)
		}
		if base == id && effort == "" {
			return efforts
		}
		return nil
	})
}

func providerBaseEfforts(provider, model string) []string {
	base, effort := BaseModelAndEffortForProvider(provider, model)
	if base != model || effort != "" {
		return nil
	}
	return ReasoningEffortsForProviderModel(provider, base)
}

func expandEffortVariants(ids []string, efforts func(string) []string) []string {
	var expanded []string
	seen := make(map[string]bool, len(ids))
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			expanded = append(expanded, id)
		}
	}
	for _, id := range ids {
		add(id)
		for _, effort := range efforts(id) {
			add(id + "-" + effort)
		}
	}
	return expanded
}

func chatGPTCompletionCatalogIDs(cfg *config.Config, provider string) []string {
	if models, _, delegated := DelegatedModelCatalog(cfg, provider); delegated {
		return delegatedModelIDs(models, "")
	}
	return ProviderModelIDs("chatgpt")
}
