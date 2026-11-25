package composer

import (
	"testing"

	"github.com/term-llm/term-llm/internal/config"
)

func TestLocalModelProvidersIncludesActiveProvider(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"corp": {Type: config.ProviderTypeOpenAICompat, Model: "m1", Models: []string{"m1", "m2"}},
	}}
	providers := LocalModelProviders(cfg, "custom-route", func(string) bool { return false })
	var corp, active bool
	for _, provider := range providers {
		switch provider.ID {
		case "corp":
			corp = corp || len(provider.Models) > 0
		case "custom-route":
			active = true
		}
	}
	if !corp || !active {
		t.Fatalf("providers = %+v, want corp models and the active provider", providers)
	}
}
