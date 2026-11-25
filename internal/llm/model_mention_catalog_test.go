package llm

import (
	"slices"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
)

func TestMentionModelOptionsEffortOnlyPolicy(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"claude-bin": {
		Type: config.ProviderTypeClaudeBin, Model: "opus", AllowedModels: []string{"opus-high"},
	}}}
	got := MentionModelOptions(cfg, "claude-bin", []core.ModelInfo{{ID: "opus", ReasoningEfforts: []string{"low", "high"}}})
	if len(got) != 1 || got[0].ID != "opus-high" || len(got[0].Efforts) != 0 {
		t.Fatalf("options = %+v, want exact opus-high without efforts", got)
	}
}

func TestMentionModelOptionsConfiguredAlias(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"corp": {
		Type: config.ProviderTypeOpenAICompat, Model: "friendly", AllowedModels: []string{"upstream/model"},
		ModelConfigs: []config.ProviderModelConfig{{ID: "upstream/model", Alias: "friendly", ReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"}},
	}}}
	got := MentionModelOptions(cfg, "corp", []core.ModelInfo{{ID: "upstream/model"}, {ID: "denied"}, {ID: "upstream/model-high"}})
	if len(got) != 1 || got[0].ID != "upstream/model" || got[0].Name != "friendly" || !slices.Equal(got[0].Efforts, []string{"low", "high"}) || got[0].DefaultEffort != "high" {
		t.Fatalf("options = %+v, want one allowed base with configured metadata", got)
	}
}

func TestMentionModelOptionsKeepsSuffixLikeExactName(t *testing.T) {
	got := MentionModelOptions(nil, "openai", []core.ModelInfo{{ID: "gpt-5.1-codex-max"}})
	if len(got) != 1 || got[0].ID != "gpt-5.1-codex-max" {
		t.Fatalf("options = %+v, want exact model ID", got)
	}
}
