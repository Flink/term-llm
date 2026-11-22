package llm

import (
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
)

// CheckProviderModelAllowed enforces the operator's provider-wide model ceiling.
// Configured aliases are matched by upstream identity; moving backend aliases
// such as Claude Code's "opus" are not resolved to a pinned generation.
func CheckProviderModelAllowed(cfg *config.Config, provider, model string) error {
	return providerModelCheck(cfg, provider)(model)
}

// providerModelCheck shares one catalog snapshot and validated allowlist
// across a batch. Unrestricted providers do not need a catalog read.
func providerModelCheck(cfg *config.Config, provider string) func(string) error {
	if cfg == nil || len(cfg.Providers[provider].AllowedModels) == 0 {
		return func(string) error { return nil }
	}
	pc := cfg.Providers[provider]
	if err := config.ValidateProviderAllowedModels(provider, pc.AllowedModels); err != nil {
		return func(string) error { return err }
	}
	catalog, _, _ := DelegatedModelCatalog(cfg, provider)
	allowed := ProviderAllowedModelIDs(cfg, provider)
	return func(model string) error {
		selected := providerPolicyModel(cfg, provider, model)
		for _, base := range allowed {
			if isModelOrEffortVariantForConfig(cfg, provider, selected, base, catalog) {
				return nil
			}
		}
		return fmt.Errorf("model %q is not allowed by providers.%s.allowed_models (allowed: %s)", provider+":"+selected, provider, strings.Join(pc.AllowedModels, ", "))
	}
}

func providerPolicyModel(cfg *config.Config, provider, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		pc := cfg.Providers[provider]
		model = strings.TrimSpace(pc.Model)
		if model == "" {
			model = config.DefaultProviderModel(string(config.InferProviderType(provider, pc.Type)))
		}
	}
	return config.UpstreamModelForProviderModel(cfg, provider, model)
}

// The existing request wrapper owns dispatch and forwards optional capabilities,
// so adding admission here preserves CLI tool loops, state and helper forks.
func wrapWithProviderModelPolicy(cfg *config.Config, name, model string, provider core.Provider, retry RetryConfig) core.Provider {
	wrapped := &RetryProvider{inner: provider, config: normalizeRetryConfig(retry)}
	pc := cfg.Providers[name]
	if len(pc.AllowedModels) == 0 {
		return wrapped
	}
	pc.AllowedModels = append([]string(nil), pc.AllowedModels...)
	pc.Models = append([]string(nil), pc.Models...)
	pc.ModelConfigs = append([]config.ProviderModelConfig(nil), pc.ModelConfigs...)
	for i := range pc.ModelConfigs {
		pc.ModelConfigs[i].ReasoningEfforts = append([]string(nil), pc.ModelConfigs[i].ReasoningEfforts...)
	}
	// Freeze the selected default: later ApplyOverrides calls must not change the
	// fallback identity of a provider already constructed for another session.
	pc.Model = providerPolicyModel(cfg, name, model)
	snapshot := &config.Config{Providers: map[string]config.ProviderConfig{name: pc}}
	wrapped.checkModel = func(model, effort string) error {
		model = providerPolicyModel(snapshot, name, model)
		if effort = strings.TrimSpace(effort); effort != "" {
			base, _ := BaseModelAndEffortForProvider(name, model)
			if entry, ok := config.ModelConfigForProviderModel(snapshot, name, model); ok {
				base = trimDeclaredModelEffort(model, entry)
			}
			model = base + "-" + effort
		}
		return CheckProviderModelAllowed(snapshot, name, model)
	}
	return wrapped
}
