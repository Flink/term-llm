package composer

import (
	"strings"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/mentions"
)

// LocalModelProviders lists providers with their locally known catalogs in
// picker order. An empty catalog still offers the provider so bare-prefix
// recognition works, and the session's active provider is always included even
// when generic discovery is narrower. hasLocalCredentials reports built-in
// login state and is passed through so callers share one cached probe.
func LocalModelProviders(cfg *config.Config, activeProvider string, hasLocalCredentials func(string) bool) []mentions.ModelProvider {
	providers := make([]mentions.ModelProvider, 0)
	seen := make(map[string]bool)
	for _, info := range llm.AvailableProviders(cfg, hasLocalCredentials) {
		if info.Name == "" || seen[info.Name] {
			continue
		}
		seen[info.Name] = true
		providers = append(providers, mentions.ModelProvider{ID: info.Name, Models: llm.LocalMentionModelOptions(cfg, info.Name)})
	}
	if active := strings.TrimSpace(activeProvider); active != "" && !seen[active] {
		providers = append(providers, mentions.ModelProvider{ID: active, Models: llm.LocalMentionModelOptions(cfg, active)})
	}
	return providers
}
