package llm

import (
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
)

// LocalDiscoveryModels returns configured and cached model facts without live discovery.
func LocalDiscoveryModels(cfg *config.Config, provider string) []core.ModelInfo {
	var models []core.ModelInfo
	cached, _, delegated := DelegatedModelCatalog(cfg, provider)
	models = append(models, cached...)
	if cfg != nil {
		pc := cfg.Providers[provider]
		models = appendDiscoveryIDs(models, pc.Models)
		models = appendDiscoveryIDs(models, []string{pc.Model})
	}
	if !delegated {
		models = appendDiscoveryIDs(models, ResolveProviderModelIDs(provider))
	}
	return models
}

func appendDiscoveryIDs(models []core.ModelInfo, ids []string) []core.ModelInfo {
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			models = append(models, core.ModelInfo{ID: id})
		}
	}
	return models
}

// DiscoveryModelCatalog normalizes model facts for pickers that show efforts separately.
func DiscoveryModelCatalog(cfg *config.Config, provider string, raw []core.ModelInfo) []core.ModelInfo {
	models := ProviderModelsForDiscovery(cfg, provider, raw)
	split := discoveryEffortSplitter(cfg, provider)
	byID := make(map[string]core.ModelInfo, len(models))
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if _, found := byID[model.ID]; model.ID == "" || found {
			continue
		}
		ids = append(ids, model.ID)
		byID[model.ID] = model
	}
	kept := ids[:0]
	for _, id := range ids {
		base := discoveryModelBase(cfg, provider, id, split)
		if _, found := byID[base]; base != id && found {
			continue
		}
		kept = append(kept, id)
	}
	defaultModel := ""
	if cfg != nil {
		defaultModel = discoveryModelBase(cfg, provider, strings.TrimSpace(cfg.Providers[provider].Model), split)
	}
	check := providerModelCheck(cfg, provider)
	ordered := make([]core.ModelInfo, 0, len(kept))
	for _, id := range SortModelIDsByPopularity(provider, defaultModel, kept) {
		if model, ok := byID[id]; ok {
			ordered = append(ordered, discoveryModelFacts(cfg, provider, model, check, split))
		}
	}
	return ordered
}

func discoveryModelBase(cfg *config.Config, provider, id string, split func(string) (string, string)) string {
	if entry, ok := config.ModelConfigForProviderModel(cfg, provider, id); ok {
		return trimDeclaredModelEffort(id, entry)
	}
	if base, _ := split(id); base != "" {
		return base
	}
	return id
}

func discoveryModelFacts(cfg *config.Config, provider string, model core.ModelInfo, check func(string) error, split func(string) (string, string)) core.ModelInfo {
	if entry, ok := config.ModelConfigForProviderModel(cfg, provider, model.ID); ok {
		if len(model.ReasoningEfforts) == 0 {
			model.ReasoningEfforts = entry.ReasoningEfforts
		}
		if model.DefaultReasoningEffort == "" {
			model.DefaultReasoningEffort = entry.DefaultReasoningEffort
		}
	}
	if len(model.ReasoningEfforts) == 0 {
		model.ReasoningEfforts = ReasoningEffortsForProviderModel(provider, model.ID)
	}
	model.DefaultReasoningEffort = strings.TrimSpace(model.DefaultReasoningEffort)
	if providerModelsRestricted(cfg, provider) {
		model.ReasoningEfforts = filterProviderReasoningEfforts(cfg, provider, model.ID, model.ReasoningEfforts, check, split)
		if model.DefaultReasoningEffort != "" && len(filterProviderReasoningEfforts(cfg, provider, model.ID, []string{model.DefaultReasoningEffort}, check, split)) == 0 {
			model.DefaultReasoningEffort = ""
		}
	}
	return model
}

// MentionModelOptions turns model facts into safe, insertable references.
func MentionModelOptions(cfg *config.Config, provider string, raw []core.ModelInfo) []core.ModelMentionOption {
	models := DiscoveryModelCatalog(cfg, provider, raw)
	split := discoveryEffortSplitter(cfg, provider)
	check := providerModelCheck(cfg, provider)
	options := make([]core.ModelMentionOption, 0, len(models))
	for _, model := range models {
		name := model.DisplayName
		if entry, ok := config.ModelConfigForProviderModel(cfg, provider, model.ID); ok && strings.TrimSpace(entry.Alias) != "" {
			name = config.DisplayModelForProviderModel(cfg, provider, model.ID)
		}
		var efforts []string
		if discoveryModelBase(cfg, provider, model.ID, split) == model.ID {
			for _, effort := range model.ReasoningEfforts {
				if effort = strings.TrimSpace(effort); effort != "" && check(model.ID+"-"+effort) == nil {
					efforts = append(efforts, effort)
				}
			}
		}
		options = append(options, core.ModelMentionOption{ID: model.ID, Name: name, Alias: IsMovingModelAlias(cfg, provider, model.ID), Efforts: efforts, DefaultEffort: model.DefaultReasoningEffort})
	}
	return options
}

// LocalMentionModelOptions builds model mentions from the local catalog.
func LocalMentionModelOptions(cfg *config.Config, provider string) []core.ModelMentionOption {
	return MentionModelOptions(cfg, provider, LocalDiscoveryModels(cfg, provider))
}
