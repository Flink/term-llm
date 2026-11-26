package ui

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
	"github.com/term-llm/term-llm/internal/llm/mockprovider"
)

func TestFormatContextMeter(t *testing.T) {
	tests := []struct {
		used, window int
		want         string
	}{
		{0, 0, ""},
		{0, 200_000, "~0/200K"},
		{164_500, 1_048_576, "~165K/1M"},
		{42_000, 0, "~42K"},
	}
	for _, tc := range tests {
		if got := FormatContextMeter(tc.used, tc.window); got != tc.want {
			t.Errorf("FormatContextMeter(%d, %d) = %q, want %q", tc.used, tc.window, got, tc.want)
		}
	}
}

func TestContextMeterAndStatsForEngine(t *testing.T) {
	p := mockprovider.NewMockProvider("agy-bin").WithCapabilities(core.Capabilities{ManagesOwnContext: true})
	eng := engine.NewEngine(p, nil)
	eng.ConfigureContextManagement(p, "agy-bin", "gemini-4-neon", false)
	eng.SetContextEstimateBaseline(96_400, 2)
	eng.RestoreContextWindow("agy-bin", "gemini-4-neon", 1_048_576)

	if got := ContextMeterForEngine(eng, true, true, nil); got != "~96K/1M" {
		t.Fatalf("managed streaming meter = %q, want ~96K/1M", got)
	}
	used, limit := ContextStatsForEngine(eng, "agy-bin", "gemini-4-neon", func() int { return 123 })
	if used != 96_400 || limit != 1_048_576 {
		t.Fatalf("ContextStatsForEngine = (%d, %d), want (96400, 1048576)", used, limit)
	}

	// Non-managed engine without a known input limit stays hidden even with a baseline.
	nonManaged := engine.NewEngine(mockprovider.NewMockProvider("mock"), nil)
	nonManaged.SetContextEstimateBaseline(50_000, 2)
	if got := ContextMeterForEngine(nonManaged, false, false, nil); got != "" {
		t.Fatalf("non-managed unknown-limit meter = %q, want empty", got)
	}

	// Non-managed engine with known limit uses estimate while streaming.
	nonManaged.SetContextTracking(200_000)
	if got := ContextMeterForEngine(nonManaged, false, true, func() int { return 12_000 }); got != "~12K/200K" {
		t.Fatalf("non-managed streaming meter = %q, want ~12K/200K", got)
	}
	if got := ContextMeterForEngine(nonManaged, false, false, func() int { return 12_000 }); got != "~50K/200K" {
		t.Fatalf("non-managed idle meter = %q, want ~50K/200K", got)
	}
}

func TestRenderContextGrid(t *testing.T) {
	grid := RenderContextGrid(500, 1000, 800, 900)
	lines := strings.Split(grid, "\n")
	if len(lines) != 6 {
		t.Fatalf("grid rows = %d, want 6", len(lines))
	}
	if !strings.Contains(grid, "■") || !strings.Contains(grid, "×") || !strings.Contains(grid, "!") || !strings.Contains(grid, "□") {
		t.Fatalf("grid missing expected markers:\n%s", grid)
	}
}
