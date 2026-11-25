package chat

import tea "charm.land/bubbletea/v2"

func (m *Model) emojiCursor(draft string) int {
	if m.directShellComposerActive() || m.directShellRun != nil || m.completions.IsVisible() ||
		m.dialog.IsOpen() || m.commit != nil || m.sessionTransition != nil || m.mentionPopup.IsVisible() ||
		m.approvalModel != nil || m.askUserModel != nil || m.handoverPreview != nil || !m.textarea.Focused() || m.textarea.HasSelection() {
		return -1
	}
	return textareaCursorByteOffset(draft, m.textarea.Line(), m.textarea.Column())
}

func (m *Model) updateComposerPopups() tea.Cmd {
	cmd := m.updateMentionQuery()
	m.updateEmojiPopup()
	return cmd
}

func (m *Model) updateEmojiPopup() {
	draft := m.textarea.Value()
	m.emojiPopup.Refresh(draft, m.emojiCursor(draft))
}

func (m *Model) emojiPopupActive() bool {
	if !m.emojiPopup.HasMatches() {
		return false
	}
	draft := m.textarea.Value()
	return m.emojiPopup.Active(draft, m.emojiCursor(draft))
}
