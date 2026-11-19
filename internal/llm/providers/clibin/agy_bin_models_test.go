package clibin

import (
	"context"
	"testing"

	"github.com/term-llm/term-llm/internal/cache"
)

func TestParseAgyModelsOutput(t *testing.T) {
	output := "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
		"gemini-3.8-flash-medium\tGemini 3.8 Flash (Medium)\n" +
		"gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n" +
		"gemini-4-neon-high\tGemini 4 Neon (High)\n" +
		"gemini-4-neon-xhigh\tGemini 4 Neon (XHigh)\n" +
		"gemini-4-neon-low\tGemini 4 Neon (Low)\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n" +
		"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

	models := parseAgyModelsOutput(output)
	wantIDs := []string{
		"gemini-3.8-flash",
		"gemini-4-neon",
		"claude-sonnet-4-6",
		"gpt-oss-120b",
	}
	if got := modelIDs(models); !equalSlice(got, wantIDs) {
		t.Fatalf("parseAgyModelsOutput IDs = %v, want %v", got, wantIDs)
	}
	if got := models[0].DisplayName; got != "Gemini 3.8 Flash" {
		t.Fatalf("models[0].DisplayName = %q, want %q", got, "Gemini 3.8 Flash")
	}
	if !equalSlice(models[0].ReasoningEfforts, []string{"low", "medium", "high"}) {
		t.Fatalf("models[0].ReasoningEfforts = %v, want [low medium high]", models[0].ReasoningEfforts)
	}
	if !equalSlice(models[1].ReasoningEfforts, []string{"low", "high", "xhigh"}) {
		t.Fatalf("models[1].ReasoningEfforts = %v, want [low high xhigh]", models[1].ReasoningEfforts)
	}
	if got := models[2].DisplayName; got != "Claude Sonnet 4.6 (Thinking)" {
		t.Fatalf("models[2].DisplayName = %q, want %q", got, "Claude Sonnet 4.6 (Thinking)")
	}
	if len(models[2].ReasoningEfforts) != 0 {
		t.Fatalf("models[2].ReasoningEfforts = %v, want none", models[2].ReasoningEfforts)
	}
	if !equalSlice(models[3].ReasoningEfforts, []string{"medium"}) {
		t.Fatalf("models[3].ReasoningEfforts = %v, want [medium]", models[3].ReasoningEfforts)
	}
}

func TestParseAgyEffort(t *testing.T) {
	tests := []struct {
		input      string
		wantModel  string
		wantEffort string
	}{
		{"gemini-4-neon-xhigh", "gemini-4-neon", "xhigh"},
		{"gemini-4-neon-high", "gemini-4-neon", "high"},
		{"claude-opus-5-5-medium", "claude-opus-5-5", "medium"},
		{"gpt-oss-120b-medium", "gpt-oss-120b", "medium"},
		{"gemini-pro-orchestrator-neon", "gemini-pro-orchestrator-neon", ""},
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking", ""},
		{"high", "high", ""},
	}
	for _, tt := range tests {
		gotModel, gotEffort := parseAgyEffort(tt.input)
		if gotModel != tt.wantModel || gotEffort != tt.wantEffort {
			t.Errorf("parseAgyEffort(%q) = (%q, %q), want (%q, %q)", tt.input, gotModel, gotEffort, tt.wantModel, tt.wantEffort)
		}
	}
}

func TestGetCachedAgyBinModelInfosUpgradesLegacyFlatCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := cache.WriteModelCache(agyBinModelCacheKey, []string{
		"gemini-4-neon-high",
		"gemini-4-neon-xhigh",
		"gemini-4-neon-low",
		"gemini-pro-orchestrator-neon",
	}); err != nil {
		t.Fatalf("WriteModelCache: %v", err)
	}
	infos := GetCachedAgyBinModelInfos()
	if got := modelIDs(infos); !equalSlice(got, []string{"gemini-4-neon", "gemini-pro-orchestrator-neon"}) {
		t.Fatalf("upgraded cached IDs = %v", got)
	}
	if !equalSlice(infos[0].ReasoningEfforts, []string{"low", "high", "xhigh"}) {
		t.Fatalf("upgraded efforts = %v, want [low high xhigh]", infos[0].ReasoningEfforts)
	}
	if len(infos[1].ReasoningEfforts) != 0 {
		t.Fatalf("orchestrator efforts = %v, want none", infos[1].ReasoningEfforts)
	}
}

func TestRefreshAgyBinModelsIfStaleReusesFreshCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	original := agyModelsCommandOutput
	t.Cleanup(func() { agyModelsCommandOutput = original })

	calls := 0
	agyModelsCommandOutput = func(context.Context, *AgyBinProvider) ([]byte, error) {
		calls++
		return []byte("gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n"), nil
	}

	for range 2 {
		if err := RefreshAgyBinModelsIfStale(context.Background(), "", nil); err != nil {
			t.Fatalf("RefreshAgyBinModelsIfStale: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("agy models calls = %d, want 1", calls)
	}
}

func TestAgyBinListModelsParsesAndCaches(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	original := agyModelsCommandOutput
	t.Cleanup(func() { agyModelsCommandOutput = original })
	agyModelsCommandOutput = func(context.Context, *AgyBinProvider) ([]byte, error) {
		return []byte("gemini-3.8-flash-high\tGemini 3.8 Flash (High)\nclaude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n"), nil
	}

	models, err := NewAgyBinProvider("", nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"gemini-3.8-flash", "claude-sonnet-4-6"}
	if got := modelIDs(models); !equalSlice(got, want) {
		t.Fatalf("ListModels IDs = %v, want %v", got, want)
	}
	if !equalSlice(models[0].ReasoningEfforts, []string{"high"}) {
		t.Fatalf("ListModels efforts = %v, want [high]", models[0].ReasoningEfforts)
	}
	if got := GetCachedAgyBinModels(); !equalSlice(got, want) {
		t.Fatalf("cached IDs = %v, want %v", got, want)
	}
}
