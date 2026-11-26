package ui

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/llm"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/engine"
)

// FormatContextMeter formats a coarse context usage label for terminal status
// lines (e.g., "~165K/1M" when window is known, "~42K" when only token count
// is known, or "" when neither is available).
func FormatContextMeter(used, window int) string {
	if window > 0 {
		return fmt.Sprintf("~%s/%s", cmp.Or(llm.FormatTokenCount(used), "0"), llm.FormatTokenCount(window))
	}
	if label := llm.FormatTokenCount(used); label != "" {
		return "~" + label
	}
	return ""
}

// ContextMeterForEngine formats the active status-line context meter for eng.
// Providers that manage their own context display their reported/restored
// ContextUsage; engine-tracked providers require a known InputLimit and fall
// back to estimate when streaming or before the first response.
func ContextMeterForEngine(eng *engine.Engine, managesOwnContext, streaming bool, estimate func() int) string {
	if eng == nil {
		return ""
	}
	if managesOwnContext {
		reading := eng.ContextUsage()
		return FormatContextMeter(reading.UsedTokens, reading.Window)
	}
	limit := eng.InputLimit()
	if limit <= 0 {
		return ""
	}
	contextTokens := 0
	if !streaming {
		contextTokens = eng.LastTotalTokens()
	}
	if contextTokens <= 0 && estimate != nil {
		contextTokens = estimate()
	}
	return FormatContextMeter(contextTokens, limit)
}

// ContextStatsForEngine resolves the display token count and input limit for
// stats views, preferring live or restored engine readings and never guessing
// CLI windows from direct-API tables.
func ContextStatsForEngine(eng *engine.Engine, providerKey, modelName string, estimate func() int) (used, limit int) {
	var reading core.ContextUsage
	if eng != nil {
		reading = eng.ContextUsage()
	}
	limit = llm.SessionDisplayInputLimit(providerKey, modelName, reading.Window)
	used = max(0, reading.UsedTokens)
	if used == 0 && estimate != nil {
		used = max(0, estimate())
	}
	return used, limit
}

// RenderContextGrid renders the 48x6 context pressure grid showing current
// usage (■), soft/hard compaction buffers (×/!), and free space (□).
func RenderContextGrid(used, limit, softThreshold, hardThreshold int) string {
	const cols = 48
	const rows = 6
	total := cols * rows
	usedCells := 0
	softBufferCells := 0
	hardBufferCells := 0
	if limit > 0 {
		usedCells = int(float64(used) / float64(limit) * float64(total))
		if used > 0 && usedCells == 0 {
			usedCells = 1
		}
		if softThreshold > 0 {
			softBufferCells = int(float64(max(0, limit-softThreshold)) / float64(limit) * float64(total))
		}
		if hardThreshold > 0 {
			hardBufferCells = int(float64(max(0, limit-hardThreshold)) / float64(limit) * float64(total))
		}
	}
	var b strings.Builder
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			i := r*cols + c
			switch {
			case i < usedCells:
				b.WriteRune('■')
			case hardBufferCells > 0 && i >= total-hardBufferCells:
				b.WriteRune('!')
			case softBufferCells > 0 && i >= total-softBufferCells:
				b.WriteRune('×')
			default:
				b.WriteRune('□')
			}
		}
		if r < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
