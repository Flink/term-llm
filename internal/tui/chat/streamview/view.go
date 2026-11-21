package streamview

import (
	"maps"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/term-llm/term-llm/internal/ui"
)

// ClickHost carries the viewport state needed to resolve a script header click.
type ClickHost struct {
	AltScreen        bool
	Blocked          bool
	Width            int
	ViewportHeight   int
	ViewportOffset   int
	FallbackExpanded bool
	Invalidate       func(fromHistory bool)
}

// RenderHost carries the live stream state needed to render the streaming tail
// and completed retained segments with script header metadata.
type RenderHost struct {
	Tracker        *ui.ToolTracker
	Width          int
	AltScreen      bool
	ToolsExpanded  bool
	Markdown       func(string, int) string
	ImageRenderer  ui.ImageArtifactRenderer
	ReasoningBlock func() string
	Spinner        string
	Phase          string
	Elapsed        time.Duration
	Tokens         int
	WavePos        int
	PausedForUI    bool
	RetryStatus    string
	Styles         *ui.Styles
}

// State tracks per-call script expansion overrides as well as the render-derived
// hit rows and completed-tail caches for the current frame.
type State struct {
	overrides   map[string]bool
	historyHits map[int]ui.ScriptHeaderHit
	tailHits    map[int]ui.ScriptHeaderHit

	completed        string
	completedHits    []ui.ScriptHeaderHit
	completedVersion uint64

	streamingHits      []ui.ScriptHeaderHit
	completedStreamHit []ui.ScriptHeaderHit
}

// ScriptOptions returns render options carrying the current per-call overrides.
func (s *State) ScriptOptions(width int) ui.ScriptRenderOptions {
	return ui.ScriptRenderOptions{Width: width, Expanded: s.overrides}
}

// Overrides returns the per-call expansion overrides for renderers. Callers
// must treat the returned map as read-only.
func (s *State) Overrides() map[string]bool {
	if s == nil {
		return nil
	}
	return s.overrides
}

// Hits returns a copy of the clickable script header rows for the visible frame.
func (s *State) Hits() map[int]ui.ScriptHeaderHit {
	hits := make(map[int]ui.ScriptHeaderHit, len(s.historyHits)+len(s.tailHits))
	maps.Copy(hits, s.historyHits)
	maps.Copy(hits, s.tailHits)
	return hits
}

// Clear drops per-call overrides and every cached hit, as a session replacement
// must not carry script UI state into another conversation.
func (s *State) Clear() {
	if s == nil {
		return
	}
	*s = State{}
}

// InvalidateCompleted drops only the cached unflushed completed-segment render.
func (s *State) InvalidateCompleted() {
	if s == nil {
		return
	}
	s.completed = ""
	s.completedHits = nil
	s.completedVersion = 0
}

// Completed returns the cached completed-segment render and its tracker version.
func (s *State) Completed() (string, uint64) {
	if s == nil {
		return "", 0
	}
	return s.completed, s.completedVersion
}

// Capture records the clickable rows for the frame whose content was just sent
// to the viewport. History hits already use absolute history lines; live-tail
// hits are offset past the rendered history.
// historyLineCount includes the final (possibly empty) split history line.
func (s *State) Capture(historyHits map[int]ui.ScriptHeaderHit, historyLineCount int, streaming, completedVisible bool) {
	if s == nil {
		return
	}
	s.historyHits = historyHits
	s.tailHits = nil
	tail := s.completedStreamHit
	if streaming {
		tail = s.streamingHits
	} else if !completedVisible {
		return
	}
	if len(tail) == 0 {
		return
	}
	s.tailHits = make(map[int]ui.ScriptHeaderHit, len(tail))
	baseLine := max(0, historyLineCount-1)
	for _, hit := range tail {
		hit.Line += baseLine
		s.tailHits[hit.Line] = hit
	}
}

// HandleClick toggles the script call whose rendered header span contains the
// click. It reports whether the event was consumed.
func (s *State) HandleClick(host ClickHost, msg tea.MouseMsg) bool {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || !host.AltScreen || host.Blocked {
		return false
	}
	if click.Y < 0 || click.Y >= host.ViewportHeight || click.X < 0 || (host.Width > 0 && click.X >= host.Width) {
		return false
	}
	line := host.ViewportOffset + click.Y
	hit, ok := s.tailHits[line]
	fromHistory := !ok
	if fromHistory {
		hit, ok = s.historyHits[line]
	}
	if !ok || click.X < hit.ColumnStart || click.X >= hit.ColumnEnd || strings.TrimSpace(hit.ToolCallID) == "" {
		return false
	}
	s.toggle(hit.ToolCallID, hit.Expanded, host.FallbackExpanded)
	if host.Invalidate != nil {
		host.Invalidate(fromHistory)
	}
	return true
}

func (s *State) toggle(callID string, expanded, fallback bool) {
	next := !expanded
	if next == fallback {
		if s.overrides != nil {
			delete(s.overrides, callID)
			if len(s.overrides) == 0 {
				s.overrides = nil
			}
		}
		return
	}
	if s.overrides == nil {
		s.overrides = make(map[string]bool)
	}
	s.overrides[callID] = next
}

// RenderTail renders the live streaming tail, including active reasoning, the
// completed segments, and the pending indicator. Script header hits are captured
// during the same render pass.
func (s *State) RenderTail(host RenderHost) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	var scriptHits []ui.ScriptHeaderHit

	content, completedHits := s.completedContent(host)
	if content != "" {
		b.WriteString(content)
	}
	scriptHits = append(scriptHits, completedHits...)
	s.appendReasoningBlock(&b, host)
	scriptHits = append(scriptHits, s.appendIndicator(&b, host)...)

	s.streamingHits = scriptHits
	return b.String()
}

func (s *State) appendReasoningBlock(b *strings.Builder, host RenderHost) {
	if host.ReasoningBlock == nil {
		return
	}
	activeReasoning := host.ReasoningBlock()
	if activeReasoning == "" {
		return
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
		if strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
	} else if b.Len() == 0 && host.Tracker != nil && host.Tracker.HasFlushed {
		b.WriteString(host.Tracker.FlushLeadingSeparator(ui.SegmentReasoning))
	}
	b.WriteString(strings.TrimLeft(activeReasoning, "\n"))
}

func (s *State) appendIndicator(b *strings.Builder, host RenderHost) []ui.ScriptHeaderHit {
	if host.PausedForUI {
		return nil
	}
	wavePos := host.WavePos
	var active []*ui.Segment
	if host.Tracker != nil {
		active = host.Tracker.ActiveSegments()
	}

	hasContent := b.Len() > 0
	if hasContent {
		if len(active) > 0 && host.Tracker != nil {
			completed := host.Tracker.CompletedSegments()
			if len(completed) > 0 {
				b.WriteString(ui.SegmentSeparatorBetween(completed[len(completed)-1], active[0]))
			}
		} else {
			b.WriteString("\n")
		}
	}

	indicator, hits := s.indicator(host, active, wavePos, hasContent)
	if len(hits) > 0 {
		baseLine := strings.Count(b.String(), "\n")
		for i := range hits {
			hits[i].Line += baseLine
		}
	}
	b.WriteString(indicator)
	b.WriteString("\n")

	if host.RetryStatus != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(host.Styles.Theme().Warning).Render("⚠ " + host.RetryStatus))
		b.WriteString("\n")
	}
	return hits
}

func (s *State) completedContent(host RenderHost) (string, []ui.ScriptHeaderHit) {
	if host.Tracker == nil {
		return "", nil
	}
	if s.completedVersion == host.Tracker.Version {
		return s.completed, s.completedHits
	}
	s.completedHits = nil
	opts := ui.ScriptRenderOptions{
		Width:    host.Width,
		Expanded: s.overrides,
		Hits:     &s.completedHits,
	}
	s.completed = host.Tracker.RenderUnflushedWithImageRendererAndScripts(host.Width, host.Markdown, host.AltScreen, host.ImageRenderer, &opts)
	s.completedVersion = host.Tracker.Version
	return s.completed, s.completedHits
}

func (s *State) indicator(host RenderHost, active []*ui.Segment, wavePos int, hasContent bool) (string, []ui.ScriptHeaderHit) {
	hasFlushed := false
	lastType := ui.SegmentText
	lastPlan := false
	if host.Tracker != nil {
		hasFlushed = !hasContent && host.Tracker.HasFlushed
		lastType = host.Tracker.LastFlushedType
		lastPlan = host.Tracker.LastFlushedPlan
	}
	var hits []ui.ScriptHeaderHit
	indicator := ui.StreamingIndicator{
		Spinner:         host.Spinner,
		Phase:           host.Phase,
		Elapsed:         host.Elapsed,
		Tokens:          host.Tokens,
		ShowCancel:      true,
		HideProgress:    true,
		Segments:        active,
		WavePos:         wavePos,
		Width:           host.Width,
		RenderMarkdown:  host.Markdown,
		ToolsExpanded:   host.ToolsExpanded,
		ScriptExpansion: s.overrides,
		ScriptHits:      &hits,
		HasFlushed:      hasFlushed,
		LastFlushedType: lastType,
		LastFlushedPlan: lastPlan,
	}
	return indicator.Render(host.Styles), hits
}

// RenderCompleted renders retained alt-screen stream segments and caches the
// script header hits for that exact output.
func (s *State) RenderCompleted(host RenderHost, segments []*ui.Segment) string {
	if s == nil {
		return ""
	}
	var hits []ui.ScriptHeaderHit
	opts := ui.ScriptRenderOptions{
		Width:    host.Width,
		Expanded: s.overrides,
		Hits:     &hits,
	}
	content := ui.RenderSegmentsWithScriptOptions(segments, host.Width, -1, host.Markdown, true, host.ToolsExpanded, host.ImageRenderer, &opts)
	s.completedStreamHit = hits
	return content
}
