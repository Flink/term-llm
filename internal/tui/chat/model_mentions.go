package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/tui/chat/composer"
)

// Model mentions (@model:/@m: and provider prefixes) share the mention popup but
// never touch the file index or agent catalog. They resolve against a local
// provider catalog built once per session, and a selection inserts ordinary text
// such as `claude-bin:opus-medium`: no model switch, attachment, or agent run.

// mentionModelsFirst reports whether model rows precede agent and file rows:
// scoped and provider-qualified queries know their model before other sources.
func mentionModelsFirst(active, owned bool, token mentions.ModelToken) bool {
	return owned || active && strings.Contains(token.Query, ":")
}

// modelMentionCatalog returns the provider catalog used to recognize and complete
// model mentions. Discovery is local (configured models plus cached facts),
// matching the /model picker, so no keystroke probes a login or the network. The
// result is cached for the session.
func (m *Model) modelMentionCatalog() []mentions.ModelProvider {
	if m == nil {
		return nil
	}
	if m.modelMentionLoaded {
		return m.modelMentionProviders
	}
	m.modelMentionLoaded = true
	if m.modelMentionResolver != nil {
		m.modelMentionProviders = m.modelMentionResolver()
		return m.modelMentionProviders
	}
	m.modelMentionProviders = composer.LocalModelProviders(m.config, m.providerKey, providerHasLocalCredentials)
	return m.modelMentionProviders
}

// invalidateModelMentionCatalog drops the session snapshot so the next mention
// rebuilds it from current configuration and cached discovery. It never probes.
func (m *Model) invalidateModelMentionCatalog() {
	m.modelMentionLoaded = false
	m.modelMentionProviders = nil
}

// activeModelMentionToken recognizes scoped and provider-prefixed model
// mentions at the byte cursor.
func (m *Model) activeModelMentionToken(text string, cursor int) (mentions.ModelToken, bool) {
	if m == nil {
		return mentions.ModelToken{}, false
	}
	return mentions.ActiveModelToken(text, cursor, m.modelMentionCatalog())
}

// modelsActionable reports whether synchronous model rows are already usable
// while the file and agent search is still pending.
func (p *mentionPopupModel) modelsActionable() bool {
	return p != nil && p.layout().ModelsActionable()
}

// modelSelectionReady reports whether the selected row is such a model row.
func (p *mentionPopupModel) modelSelectionReady() bool {
	return p != nil && p.layout().SelectedModel(p.selected)
}

func (p *mentionPopupModel) clampSelection() {
	if p == nil {
		return
	}
	if count := p.selectableCount(); p.selected >= count {
		p.selected = 0
	}
}

// insertModelMention replaces the full model token span with the chosen
// reference. Continuing rows keep the token open on its effort step; final
// rows insert plain `provider:model[-effort]` text and close the popup.
func (m *Model) insertModelMention(row mentions.ModelRow, token mentions.ModelToken) (bool, tea.Cmd) {
	old := m.textarea.Value()
	updated, cursor := composer.ReplaceMention(old, token.Start, token.End, row.InsertText, row.Continues)
	if updated == old {
		return false, nil
	}
	m.textarea.SetValue(updated)
	m.moveTextareaCursorToByteOffset(cursor)
	m.updateTextareaHeight()
	// Cancel any pending debounce before reopening on the effort step.
	m.hideMentionPopup()
	if row.Continues {
		return true, m.updateMentionQuery()
	}
	return true, nil
}
