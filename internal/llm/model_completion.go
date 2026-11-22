package llm

import (
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	rp "github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
)

func filterModelCompletions(cfg *config.Config, provider, modelPrefix string, isImage bool, models []string, providerCfg config.ProviderConfig) []string {
	splitEffort := discoveryEffortSplitter(cfg, provider)
	fastTier := completionFastTier(cfg, provider, isImage, providerCfg)
	check := providerModelCheck(cfg, provider)
	// Reveal suffixes only after their parent model name is fully typed.
	var completions []string
	for _, model := range models {
		if !isImage {
			base, effort := splitEffort(model)
			if effort != "" && !strings.HasPrefix(modelPrefix, base) {
				continue
			}
		}
		if strings.HasPrefix(model, modelPrefix) {
			completions = append(completions, provider+":"+model)
		}
		if fastTier != nil && strings.HasPrefix(modelPrefix, model) {
			fast := model + "-fast"
			if strings.HasPrefix(fast, modelPrefix) && !slices.Contains(models, fast) {
				if fastTier(fast) && check(fast) == nil {
					completions = append(completions, provider+":"+fast)
				}
			}
		}
	}
	return completions
}

func completionFastTier(cfg *config.Config, provider string, isImage bool, pc config.ProviderConfig) func(string) bool {
	if isImage || config.InferProviderType(provider, pc.Type) != config.ProviderTypeChatGPT {
		return nil
	}
	if catalog, _, delegated := DelegatedModelCatalog(cfg, provider); delegated {
		return func(model string) bool {
			_, tier := rp.ChatGPTCatalogModelServiceTier(catalog, model)
			return tier != ""
		}
	}
	return func(model string) bool {
		_, tier := rp.ChatGPTModelServiceTier(model)
		return tier != ""
	}
}
