package chat

import (
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
)

// ProviderInfo holds provider and model information
type ProviderInfo = llm.ProviderInfo

// providerHasLocalCredentials detects local login state for built-ins; tests
// replace it to avoid depending on the developer's machine.
var providerHasLocalCredentials = llm.DefaultProviderCredentials.Has

// GetAvailableProviders returns configured providers with their models in
// consistent order: the same set the web UI picker and `term-llm providers`
// treat as configured under provider_discovery (see
// config.ProviderConfiguredVia).
func GetAvailableProviders(cfg *config.Config) []ProviderInfo {
	return llm.AvailableProviders(cfg, providerHasLocalCredentials)
}

func cachedOAuthPickerModels(cfg *config.Config, provider string) ([]core.ModelInfo, bool, error) {
	if models, fresh, delegated := llm.DelegatedModelCatalog(cfg, provider); delegated {
		return models, fresh, nil
	}
	return responsesproviders.CachedChatGPTModels()
}
