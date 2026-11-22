package serve

import (
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/llm/core"
)

func appendLocalModelCatalog(configured []core.ModelInfo, cfg *config.Config, provider string) []core.ModelInfo {
	if cached, _, delegated := llm.DelegatedModelCatalog(cfg, provider); delegated {
		// Prefer catalog metadata for IDs that also appear in config.
		return append(cached, configured...)
	}
	return appendModelIDs(configured, llm.ResolveProviderModelIDs(provider))
}
