package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/tui/chat/streamview"
	"github.com/term-llm/term-llm/internal/ui"
)

func (m *Model) captureScriptClicks() {
	var hits map[int]ui.ScriptHeaderHit
	if len(m.messages) > 0 {
		hits = m.chatRenderer.ScriptLineHitsSnapshot()
	}
	m.streamView.Capture(hits, len(m.viewCache.historyLines),
		m.streaming || m.activeSkillRunCount() > 0, m.viewCache.completedStream != "")
}

// scriptRenderHost snapshots the live stream state needed by the script view
// renderer. Reasoning is rendered lazily, only for the live tail.
func (m *Model) scriptRenderHost() streamview.RenderHost {
	host := streamview.RenderHost{
		Tracker:        m.tracker,
		Width:          m.width,
		AltScreen:      m.altScreen,
		ToolsExpanded:  m.toolsExpanded,
		Markdown:       m.renderMd,
		ImageRenderer:  m.imageArtifactRenderer(),
		ReasoningBlock: m.renderCurrentReasoningBlock,
		Spinner:        m.spinner.View(),
		Phase:          m.phase,
		Elapsed:        m.visibleStreamElapsed(),
		Tokens:         m.currentTokens,
		PausedForUI:    m.pausedForExternalUI,
		RetryStatus:    m.retryStatus,
		Styles:         m.styles,
	}
	if m.tracker != nil {
		host.WavePos = m.tracker.WavePos
	}
	return host
}

// handleScriptMouseClick toggles the script call whose rendered header span
// contains the click. It must run after the sticky prompt (which owns row zero
// while visible) and before drag selection consumes the press.
func (m *Model) handleScriptMouseClick(msg tea.MouseMsg) bool {
	if click, ok := msg.(tea.MouseClickMsg); !ok || click.Button != tea.MouseLeft {
		return false
	}
	return m.streamView.HandleClick(streamview.ClickHost{
		AltScreen: m.altScreen, Blocked: m.scriptClickBlocked(), Width: m.width,
		ViewportHeight: m.viewport.Height(), ViewportOffset: m.viewport.YOffset(),
		FallbackExpanded: m.toolsExpanded, Invalidate: m.invalidateScriptExpansionView,
	}, msg)
}

// scriptClickBlocked rejects clicks while another surface owns the frame or the
// viewport contents no longer match the captured hit rows.
func (m *Model) scriptClickBlocked() bool {
	return m.quitting ||
		m.externalProcessActive ||
		m.sessionTransition != nil ||
		m.sessionSwitchPending ||
		m.resizeReflowPending ||
		m.approvalModel != nil || m.askUserModel != nil || m.handoverPreview != nil ||
		m.dialog.IsOpen() || m.commit != nil ||
		(m.completions != nil && m.completions.IsVisible()) ||
		m.mentionPopup.IsVisible() ||
		m.sideQuestion.Visible
}

func (m *Model) invalidateScriptExpansionView(fromHistory bool) {
	if fromHistory {
		m.forceHistoryRerenderPreservingBlockCache()
		return
	}
	m.streamView.InvalidateCompleted()
	m.invalidateAltScreenStreamingViewportCache()
	m.rerenderCompletedStreamFromTracker()
}

func (m *Model) renderStreamingInline() string {
	return m.streamView.RenderTail(m.scriptRenderHost())
}

func (m *Model) renderCompletedSegments(segments []*ui.Segment) string {
	return m.streamView.RenderCompleted(m.scriptRenderHost(), segments)
}
