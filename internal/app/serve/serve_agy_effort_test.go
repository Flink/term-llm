package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/providers/clibin"
)

func TestHandleModelsAgyBinExposesGroupedReasoningEfforts(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	clibin.RefreshAgyBinCacheSync([]core.ModelInfo{
		{ID: "gemini-4-neon-high", DisplayName: "Gemini 4 Neon (High)"},
		{ID: "gemini-4-neon-xhigh", DisplayName: "Gemini 4 Neon (XHigh)"},
		{ID: "gemini-4-neon-medium", DisplayName: "Gemini 4 Neon (Medium)"},
		{ID: "gemini-4-neon-low", DisplayName: "Gemini 4 Neon (Low)"},
		{ID: "claude-opus-5-5-low", DisplayName: "Claude Opus 5.5 (Low)"},
		{ID: "claude-opus-5-5-medium", DisplayName: "Claude Opus 5.5 (Medium)"},
		{ID: "claude-opus-5-5-high", DisplayName: "Claude Opus 5.5 (High)"},
		{ID: "gemini-pro-orchestrator-neon", DisplayName: "Orchestrator (Neon)"},
	})

	srv := &serveServer{
		cfgRef: &config.Config{
			DefaultProvider: "agy-bin",
			Providers: map[string]config.ProviderConfig{
				"agy-bin": {Type: config.ProviderTypeAgyBin, Model: "gemini-4-neon-xhigh"},
			},
		},
	}
	srv.setCachedModelsForProvider("agy-bin", clibin.GetCachedAgyBinModelInfos())

	rr := httptest.NewRecorder()
	srv.handleModels(rr, httptest.NewRequest(http.MethodGet, "/v1/models?provider=agy-bin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data []struct {
			ID               string   `json:"id"`
			ReasoningEfforts []string `json:"reasoning_efforts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}

	byID := make(map[string][]string, len(resp.Data))
	for _, item := range resp.Data {
		byID[item.ID] = item.ReasoningEfforts
	}
	if _, leaked := byID["gemini-4-neon-xhigh"]; leaked {
		t.Fatalf("/v1/models leaked suffixed variant gemini-4-neon-xhigh: %+v", resp.Data)
	}
	if !slices.Equal(byID["gemini-4-neon"], []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("gemini-4-neon efforts = %v, want [low medium high xhigh]", byID["gemini-4-neon"])
	}
	if !slices.Equal(byID["claude-opus-5-5"], []string{"low", "medium", "high"}) {
		t.Fatalf("claude-opus-5-5 efforts = %v, want [low medium high]", byID["claude-opus-5-5"])
	}
	if len(byID["gemini-pro-orchestrator-neon"]) != 0 {
		t.Fatalf("gemini-pro-orchestrator-neon efforts = %v, want none", byID["gemini-pro-orchestrator-neon"])
	}

	// Normalization splits suffixed agy-bin models and lets explicit effort win.
	if m, e := normalizeProviderModelEffort("agy-bin", "gemini-4-neon-xhigh", ""); m != "gemini-4-neon" || e != "xhigh" {
		t.Fatalf("normalize(gemini-4-neon-xhigh, '') = (%q, %q)", m, e)
	}
	if m, e := normalizeProviderModelEffort("agy-bin", "gemini-4-neon-high", "low"); m != "gemini-4-neon" || e != "low" {
		t.Fatalf("normalize(gemini-4-neon-high, low) = (%q, %q)", m, e)
	}
}
