package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/term-llm/term-llm/internal/tools"
)

func scriptHeaderTestSegment(id, name, source string, status ToolStatus) *Segment {
	args, _ := json.Marshal(map[string]string{"code": source, "cell_id": "cell-" + id})
	seg := &Segment{
		Type:       SegmentTool,
		ToolCallID: id,
		ToolName:   name,
		ToolArgs:   args,
		ToolStatus: status,
	}
	if status != ToolPending {
		seg.CodeMode = codeModeResultReceipt(name, fmt.Sprintf(`{"cell_id":"cell-%s","status":"completed"}`, id), nil)
	}
	return seg
}

func TestScriptHeaderSpanUsesHeaderRowOnly(t *testing.T) {
	span, end, ok := ScriptHeaderSpan("\x1b[32m▸ spawn_script · 東京\x1b[0m\nobserved body", 80)
	if !ok || span != 0 {
		t.Fatalf("span = (%d,%d,%v), want start 0 and ok", span, end, ok)
	}
	if want := ansi.StringWidth("▸ spawn_script · 東京"); end != want {
		t.Fatalf("end = %d, want %d", end, want)
	}
	if _, _, ok := ScriptHeaderSpan("▸", 3); ok {
		t.Fatal("narrow render without a disclosure must not be clickable")
	}
	if _, _, ok := ScriptHeaderSpan("", 80); ok {
		t.Fatal("empty render must not be clickable")
	}
}

func TestRenderSegmentsScriptHeaderHitsAnchorToolCallIDs(t *testing.T) {
	segments := []*Segment{
		{Type: SegmentText, Text: "▸ spawn_script decoy text, not a call", Complete: true},
		scriptHeaderTestSegment("call-a", tools.CodeExecToolName, "text('a');", ToolSuccess),
		{
			Type:       SegmentTool,
			ToolCallID: "call-shell",
			ToolName:   "read_file",
			ToolInfo:   "(a.go)",
			ToolStatus: ToolSuccess,
		},
		scriptHeaderTestSegment("call-b", tools.CodeWaitToolName, "", ToolSuccess),
	}

	var hits []ScriptHeaderHit
	opts := ScriptRenderOptions{
		Width:    80,
		Expanded: map[string]bool{"call-b": true},
		Hits:     &hits,
	}
	rendered := RenderSegmentsWithScriptOptions(segments, 80, -1, nil, false, false, nil, &opts)
	plain := StripANSI(rendered)
	lines := strings.Split(plain, "\n")
	if len(hits) != 2 {
		t.Fatalf("hits = %#v, want only the two script calls\noutput:\n%s", hits, plain)
	}

	byID := map[string]ScriptHeaderHit{}
	for _, hit := range hits {
		byID[hit.ToolCallID] = hit
		if hit.Line < 0 || hit.Line >= len(lines) {
			t.Fatalf("hit line %d outside %d rendered lines", hit.Line, len(lines))
		}
		line := StripANSI(lines[hit.Line])
		if hit.ColumnStart != 0 || hit.ColumnEnd > ansi.StringWidth(line) {
			t.Fatalf("hit %#v outside rendered line %q", hit, line)
		}
	}
	if _, ok := byID["call-shell"]; ok {
		t.Fatal("non-script tool row produced a script hit")
	}
	if first := byID["call-a"]; first.Expanded || !strings.Contains(lines[first.Line], "▸") || strings.Contains(lines[first.Line], "▾") {
		t.Fatalf("first script should stay collapsed: %#v %q", first, lines[first.Line])
	}
	second := byID["call-b"]
	if !second.Expanded || !strings.Contains(lines[second.Line], "▾") {
		t.Fatalf("per-call override should expand only the second script: %#v %q", second, lines[second.Line])
	}
	decoy := -1
	for i, line := range lines {
		if strings.Contains(line, "decoy text") {
			decoy = i
		}
	}
	if decoy < 0 {
		t.Fatalf("test fixture lost its decoy line: %q", plain)
	}
	if _, ok := byID["call-a"]; !ok || byID["call-a"].Line == decoy {
		t.Fatal("hit anchored to decoy text instead of the rendered tool call")
	}
}

func TestStreamingIndicatorScriptHitsIncludeLeadingSeparator(t *testing.T) {
	pending := scriptHeaderTestSegment("pending-call", tools.CodeExecToolName, "text(1);", ToolPending)
	var hits []ScriptHeaderHit
	indicator := StreamingIndicator{
		Segments:        []*Segment{pending},
		Width:           80,
		HasFlushed:      true,
		LastFlushedType: SegmentText,
		ScriptHits:      &hits,
	}
	rendered := StripANSI(indicator.Render(DefaultStyles()))
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one pending script header\noutput:\n%s", hits, rendered)
	}
	lines := strings.Split(rendered, "\n")
	hit := hits[0]
	if hit.ToolCallID != "pending-call" || hit.Expanded {
		t.Fatalf("pending hit = %#v", hit)
	}
	if hit.Line < 0 || hit.Line >= len(lines) || !strings.Contains(lines[hit.Line], "spawn_script") {
		t.Fatalf("pending hit line %d does not point at the header: %q\noutput:\n%s", hit.Line, lines, rendered)
	}
}

func TestScriptHeaderSpanMatchesNarrowRenderedDisclosure(t *testing.T) {
	seg := scriptHeaderTestSegment("test", tools.CodeExecToolName, "text(1);", ToolSuccess)
	for width := 1; width < 8; width++ {
		header := strings.SplitN(RenderToolSegment(seg, -1, width, false), "\n", 2)[0]
		_, end, ok := ScriptHeaderSpan(header, width)
		if disclosure := strings.Contains(header, "▸"); ok != disclosure || (ok && end != ansi.StringWidth(header)) {
			t.Fatalf("width %d, header %q: span end %d, clickable %v", width, header, end, ok)
		}
	}
}
