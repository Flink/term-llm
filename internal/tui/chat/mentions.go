package chat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/tui/chat/composer"
)

const (
	mentionQueryDebounce = 30 * time.Millisecond
	mentionMatchLimit    = 50
)

type mentionQueryMode uint8

const (
	mentionQueryGeneral mentionQueryMode = iota
	mentionQueryAgentsOnly
	mentionQueryFilesOnly
	mentionQueryModelsOnly
)

type agentMentionMatch struct {
	name string
}

type mentionPopupModel struct {
	visible      bool
	token        mentions.ActiveToken
	matches      []mentions.Match
	agentMatches []agentMentionMatch
	mode         mentionQueryMode
	agentErr     error
	// modelToken spans the whole model mention, including text to the right of
	// the caret, so accepting a row never leaves a trailing fragment.
	modelToken  mentions.ModelToken
	modelRows   []mentions.ModelRow
	modelActive bool
	// modelOwned records that the token only offers models: scoped @model:/@m:
	// and open effort steps suppress file and agent suggestions entirely.
	modelOwned     bool
	matchesRoot    string
	matchesToken   mentions.ActiveToken
	matchesCursor  int
	matchesGen     uint64
	matchesRequest uint64
	selected       int
	indexing       bool
	searching      bool
	// A refresh cleared previously visible rows; Enter still belongs to the picker.
	replacingRows bool
	err           error
}

func (p *mentionPopupModel) selectableCount() int {
	if p == nil {
		return 0
	}
	return p.layout().Count()
}

// layout maps rows onto categories. Scoped popups show only models; qualified
// provider queries put their synchronous model rows first, while bare provider
// prefixes keep files first so file suggestions retain the default selection.
func (p *mentionPopupModel) layout() composer.MentionLayout {
	return composer.MentionLayout{
		Agents: len(p.agentMatches), Models: len(p.modelRows), Files: len(p.matches),
		ModelsFirst: mentionModelsFirst(p.modelActive, p.modelOwned, p.modelToken),
	}
}

func routeMentionQuery(token mentions.ActiveToken) (mentionQueryMode, string, string) {
	if token.Agent {
		return mentionQueryAgentsOnly, "", token.Query
	}
	if token.Quoted || strings.HasPrefix(token.Query, "./") || strings.HasPrefix(token.Query, "../") || strings.ContainsAny(token.Query, `/\\`) {
		return mentionQueryFilesOnly, token.Query, ""
	}
	return mentionQueryGeneral, token.Query, token.Query
}

func rankAgentMentionMatches(capability AgentMentionCapability, query string, limit int) ([]agentMentionMatch, error) {
	if query == "" {
		return nil, nil
	}
	if capability == nil {
		return nil, errors.New("agent mentions are unavailable because spawn_agent is not enabled in this session")
	}
	names, err := capability.PermittedAgentNames()
	if err != nil {
		return nil, err
	}
	ranked := mentions.RankAgentMentionNames(names, query, limit)
	matches := make([]agentMentionMatch, len(ranked))
	for i, name := range ranked {
		matches[i] = agentMentionMatch{name: name}
	}
	return matches, nil
}

func (p *mentionPopupModel) IsVisible() bool { return p != nil && p.visible }
func (p *mentionPopupModel) invalidateMatchContext() {
	if p == nil {
		return
	}
	p.matchesRoot = ""
	// Keep matchesToken while stale rows remain visible so rendering continues to
	// describe the query that produced them. The cleared identity fields still
	// prevent those rows from being accepted.
	p.matchesCursor = 0
	p.matchesGen = 0
	p.matchesRequest = 0
}

func (p *mentionPopupModel) clearMatches() {
	if p == nil {
		return
	}
	p.matches = nil
	p.agentMatches = nil
	p.agentErr = nil
	p.matchesToken = mentions.ActiveToken{}
	p.invalidateMatchContext()
	p.selected = 0
}

// clearModelMatches drops the model suggestion rows that are computed
// synchronously, unlike the debounced file and agent matches.
func (p *mentionPopupModel) clearModelMatches() {
	if p == nil {
		return
	}
	p.modelToken = mentions.ModelToken{}
	p.modelRows = nil
	p.modelActive = false
	p.modelOwned = false
}

func (p *mentionPopupModel) Hide() {
	if p == nil {
		return
	}
	p.visible = false
	p.replacingRows = false
	p.clearMatches()
	p.clearModelMatches()
	p.err = nil
}

type mentionIndexReadyMsg struct {
	root       string
	generation uint64
	snapshot   *mentions.Snapshot
	err        error
}
type mentionDebounceMsg struct {
	root                string
	generation, request uint64
	token               mentions.ActiveToken
	cursor              int
}
type mentionMatchesMsg struct {
	root                string
	generation, request uint64
	token               mentions.ActiveToken
	cursor              int
	mode                mentionQueryMode
	matches             []mentions.Match
	agentMatches        []agentMentionMatch
	agentErr            error
}

func (m *Model) initializeMentions() {
	m.agentMentionEnabled = mentions.EnabledFromEnv()
	m.mentionEnabled = m.agentMentionEnabled
	if !m.mentionEnabled {
		return
	}
	root, err := mentionAbsoluteRoot(m.effectiveWorkingDir())
	if err != nil {
		m.mentionPopup.err = err
		return
	}
	m.mentionRoot = root
}

func mentionAbsoluteRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("project directory is unavailable")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	return filepath.Clean(abs), nil
}

func (m *Model) startMentionIndex() tea.Cmd {
	if m == nil || !m.mentionEnabled {
		return nil
	}
	if m.mentionBuildCancel != nil {
		m.mentionBuildCancel()
	}
	root, err := mentionAbsoluteRoot(m.effectiveWorkingDir())
	if err != nil {
		m.mentionIndexGeneration++
		generation := m.mentionIndexGeneration
		m.mentionRoot = ""
		return func() tea.Msg { return mentionIndexReadyMsg{generation: generation, err: err} }
	}
	ctx, cancel := context.WithCancel(m.rootContext())
	m.mentionBuildCancel = cancel
	m.mentionIndexGeneration++
	generation := m.mentionIndexGeneration
	m.mentionRoot = root
	m.mentionPopup.indexing = m.mentionIndex == nil
	return func() tea.Msg {
		snapshot, err := mentions.Build(ctx, root, mentions.DefaultBuildOptions())
		return mentionIndexReadyMsg{root: root, generation: generation, snapshot: snapshot, err: err}
	}
}

func (m *Model) handleMentionMessage(msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case mentionIndexReadyMsg:
		if msg.root != m.mentionRoot || msg.generation != m.mentionIndexGeneration {
			return true, nil
		}
		if m.mentionBuildCancel != nil {
			m.mentionBuildCancel()
			m.mentionBuildCancel = nil
		}
		m.mentionPopup.indexing = false
		if msg.err != nil {
			if m.mentionIndex == nil {
				m.mentionPopup.err = msg.err
			}
			return true, nil
		}
		m.mentionIndex = msg.snapshot
		// A scoped model popup is answered synchronously from the local
		// catalog; an index that finishes meanwhile must not replace its rows or
		// schedule a file query.
		owned := m.mentionPopup.modelOwned
		if !owned {
			// A selected leading model row keeps its index; files and agents are
			// appended after it when the new snapshot resolves.
			selected, keepModel := m.mentionPopup.selected, m.mentionPopup.modelSelectionReady()
			if m.mentionPopup.selectableCount() > 0 {
				m.mentionPopup.replacingRows = true
			}
			m.mentionPopup.clearMatches()
			if keepModel {
				m.mentionPopup.selected = selected
			}
			m.mentionPopup.searching = m.mentionPopup.visible
		}
		m.mentionPopup.err = nil
		if m.mentionPopup.visible && !owned {
			return true, m.scheduleMentionQuery(m.mentionPopup.token)
		}
		return true, nil
	case mentionDebounceMsg:
		if !m.mentionQueryCurrent(msg.root, msg.generation, msg.request, msg.token, msg.cursor) {
			return true, nil
		}
		if m.mentionPopup.modelOwned {
			return true, nil
		}
		mode, fileQuery, agentQuery := routeMentionQuery(msg.token)
		snapshot := m.mentionIndex
		capability := m.agentMentionCapability
		ctx := m.mentionQueryCtx
		return true, func() tea.Msg {
			var matches []mentions.Match
			if mode != mentionQueryAgentsOnly && snapshot != nil {
				matches = snapshot.Search(ctx, fileQuery, mentionMatchLimit)
			}
			var agentMatches []agentMentionMatch
			var agentErr error
			if mode != mentionQueryFilesOnly {
				agentMatches, agentErr = rankAgentMentionMatches(capability, agentQuery, mentionMatchLimit)
			}
			return mentionMatchesMsg{
				root: msg.root, generation: msg.generation, request: msg.request,
				token: msg.token, cursor: msg.cursor, mode: mode, matches: matches,
				agentMatches: agentMatches, agentErr: agentErr,
			}
		}
	case mentionMatchesMsg:
		if !m.mentionQueryCurrent(msg.root, msg.generation, msg.request, msg.token, msg.cursor) {
			return true, nil
		}
		if m.mentionQueryCancel != nil {
			m.mentionQueryCancel()
			m.mentionQueryCancel = nil
		}
		m.mentionPopup.searching = false
		m.mentionPopup.replacingRows = false
		m.mentionPopup.matches = msg.matches
		m.mentionPopup.agentMatches = msg.agentMatches
		m.mentionPopup.mode = msg.mode
		m.mentionPopup.agentErr = msg.agentErr
		m.mentionPopup.matchesRoot = msg.root
		m.mentionPopup.matchesToken = msg.token
		m.mentionPopup.matchesCursor = msg.cursor
		m.mentionPopup.matchesGen = msg.generation
		m.mentionPopup.matchesRequest = msg.request
		// Result replacement can change both row identity and category offsets.
		// Never carry an index from the old result set into the new one, except
		// for a leading model row whose position cannot move.
		if !m.mentionPopup.modelSelectionReady() {
			m.mentionPopup.selected = 0
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) mentionQueryCurrent(root string, generation, request uint64, token mentions.ActiveToken, cursor int) bool {
	if !m.mentionPopup.visible || root != m.mentionRoot || generation != m.mentionIndexGeneration || request != m.mentionQueryRequest {
		return false
	}
	currentCursor := textareaCursorByteOffset(m.textarea.Value(), m.textarea.Line(), m.textarea.Column())
	current, ok := mentions.ActiveTokenAt(m.textarea.Value(), currentCursor)
	return ok && current == token && currentCursor == cursor
}

func (m *Model) updateMentionQuery() tea.Cmd {
	if m == nil || !m.mentionEnabled || m.directShellComposerActive() || strings.HasPrefix(m.textarea.Value(), "/") || m.dialog.IsOpen() {
		m.hideMentionPopup()
		return nil
	}
	text := m.textarea.Value()
	cursor := textareaCursorByteOffset(text, m.textarea.Line(), m.textarea.Column())
	token, ok := mentions.ActiveTokenAt(text, cursor)
	if !ok {
		m.hideMentionPopup()
		return nil
	}
	m.completions.Hide()
	p := &m.mentionPopup
	if !p.visible {
		// A fresh mention starts from current local catalogs; keystrokes within
		// the same mention reuse the cached snapshot instead of probing.
		m.invalidateModelMentionCatalog()
	}
	previousToken, previousOwned, previousActive := p.modelToken, p.modelOwned, p.modelActive
	modelToken, modelActive := m.activeModelMentionToken(text, cursor)
	p.visible = true
	p.token = token
	p.modelRows = nil
	p.modelOwned = false
	p.modelToken = mentions.ModelToken{}
	p.modelActive = modelActive
	if modelActive {
		p.modelToken = modelToken
		p.modelOwned = modelToken.Owns()
		// Model rows are pure and cheap: compute them now so a scoped query
		// never waits for (or starts) a project index build.
		p.modelRows = mentions.CompleteModels(modelToken, m.modelMentionCatalog())
	}
	// A changed signed model query starts from its best match, and a reordered
	// layout invalidates prior row indexes. Repeated identical updates keep the
	// user's cursor where it is.
	currentFirst := mentionModelsFirst(p.modelActive, p.modelOwned, p.modelToken)
	if previousFirst := mentionModelsFirst(previousActive, previousOwned, previousToken); previousFirst != currentFirst || currentFirst && previousToken != p.modelToken {
		p.selected = 0
	}
	mode, _, _ := routeMentionQuery(token)
	if p.modelOwned {
		mode = mentionQueryModelsOnly
	}
	p.mode = mode
	// Keep the previous rows in place while the debounced query runs. Clearing
	// them here collapses the popup to its one-line searching state on every
	// keystroke, then expands it again when results arrive, which visibly flashes
	// in both inline and alt-screen rendering. The context is invalidated so the
	// retained rows cannot be accepted for the new token.
	p.invalidateMatchContext()
	p.searching = true
	if p.modelOwned {
		// Scoped model rows never wait on the file/agent debounce, so drop any
		// query scheduled for an earlier token instead of letting it resolve.
		if m.mentionQueryCancel != nil {
			m.mentionQueryCancel()
			m.mentionQueryCancel = nil
		}
		p.searching = false
		p.indexing = false
		p.replacingRows = false
		p.matches = nil
		p.agentMatches = nil
		p.agentErr = nil
		p.matchesToken = mentions.ActiveToken{}
		p.clampSelection()
		return nil
	}
	needsFiles := mode != mentionQueryAgentsOnly && m.mentionRoot != ""
	p.indexing = needsFiles && m.mentionIndex == nil
	var refresh tea.Cmd
	if needsFiles && m.mentionIndex == nil && m.mentionBuildCancel == nil {
		refresh = m.startMentionIndex()
	} else if needsFiles && m.mentionIndex != nil && m.mentionBuildCancel == nil && time.Since(m.mentionIndex.BuiltAt) >= 10*time.Second {
		refresh = m.startMentionIndex()
	}
	p.clampSelection()
	return tea.Batch(refresh, m.scheduleMentionQuery(token))
}

func (m *Model) scheduleMentionQuery(token mentions.ActiveToken) tea.Cmd {
	if m.mentionQueryCancel != nil {
		m.mentionQueryCancel()
	}
	m.mentionQueryRequest++
	request := m.mentionQueryRequest
	ctx, cancel := context.WithCancel(m.rootContext())
	m.mentionQueryCtx, m.mentionQueryCancel = ctx, cancel
	root, generation := m.mentionRoot, m.mentionIndexGeneration
	cursor := textareaCursorByteOffset(m.textarea.Value(), m.textarea.Line(), m.textarea.Column())
	return func() tea.Msg {
		timer := time.NewTimer(mentionQueryDebounce)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return mentionDebounceMsg{root: root, generation: generation, request: request, token: token, cursor: cursor}
		}
	}
}

func (m *Model) hideMentionPopup() {
	m.mentionPopup.Hide()
	if m.mentionQueryCancel != nil {
		m.mentionQueryCancel()
		m.mentionQueryCancel = nil
	}
}

func (m *Model) handleMentionPopupKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.mentionPopup.IsVisible() {
		return false, nil
	}
	count := m.mentionPopup.selectableCount()
	if step := pickerStep(msg); step != 0 {
		switch {
		case count == 0 || m.mentionPopup.searching && !m.mentionPopup.modelsActionable():
		case m.mentionPopup.searching:
			// Only the synchronous model rows are complete; retained agent and
			// file rows still belong to the previous query.
			models := len(m.mentionPopup.modelRows)
			m.mentionPopup.selected = (m.mentionPopup.selected + models + step) % models
		default:
			m.mentionPopup.selected = (m.mentionPopup.selected + count + step) % count
		}
		return true, nil
	}
	switch msg.String() {
	case "esc":
		m.hideMentionPopup()
		return true, nil
	case "enter", "tab":
		// Don't turn a pending selection into a send, even if an index swap
		// had to discard the old rows before searching the new snapshot. A fresh
		// qualified model row is computed synchronously, so it stays usable while
		// files and agents are still loading.
		if (m.mentionPopup.replacingRows || count > 0 && m.mentionPopup.searching) && !m.mentionPopup.modelSelectionReady() {
			return true, nil
		}
		if count == 0 {
			// An explicit model namespace is never a submit shortcut: enter and
			// tab only dismiss the empty picker, matching esc above.
			owned := m.mentionPopup.modelOwned
			m.hideMentionPopup()
			return owned || msg.String() == "tab", nil
		}
		accepted, cmd := m.acceptMentionSelection()
		if !accepted {
			m.hideMentionPopup()
		}
		return true, cmd
	}
	return false, nil
}

func (m *Model) acceptMentionSelection() (bool, tea.Cmd) {
	count := m.mentionPopup.selectableCount()
	if count == 0 || m.mentionPopup.selected < 0 || m.mentionPopup.selected >= count {
		return false, nil
	}
	text := m.textarea.Value()
	cursor := textareaCursorByteOffset(text, m.textarea.Line(), m.textarea.Column())
	kind, offset := m.mentionPopup.layout().RowAt(m.mentionPopup.selected)
	if kind == composer.MentionModel {
		current, active := m.activeModelMentionToken(text, cursor)
		if !active || current != m.mentionPopup.modelToken {
			return false, nil
		}
		return m.insertModelMention(m.mentionPopup.modelRows[offset], current)
	}
	current, ok := mentions.ActiveTokenAt(text, cursor)
	if !ok || current != m.mentionPopup.token || current != m.mentionPopup.matchesToken ||
		cursor != m.mentionPopup.matchesCursor || m.mentionPopup.matchesRoot != m.mentionRoot ||
		m.mentionPopup.matchesGen != m.mentionIndexGeneration || m.mentionPopup.matchesRequest != m.mentionQueryRequest {
		return false, nil
	}

	inserted := ""
	switch kind {
	case composer.MentionAgent:
		target := m.mentionPopup.agentMatches[offset]
		if m.agentMentionCapability == nil {
			_, cmd := m.showFooterError("agent mention selection is stale because spawn_agent is no longer available")
			return false, cmd
		}
		if err := m.agentMentionCapability.ValidateAgentMention(target.name); err != nil {
			_, cmd := m.showFooterError(fmt.Sprintf("cannot select %s: %v", mentions.InsertAgentText(target.name), err))
			return false, cmd
		}
		inserted = mentions.InsertAgentText(target.name)
	default:
		if m.mentionIndex == nil || offset < 0 || offset >= len(m.mentionPopup.matches) {
			return false, nil
		}
		match := m.mentionPopup.matches[offset]
		if match.Candidate < 0 || match.Candidate >= len(m.mentionIndex.Candidates) {
			return false, nil
		}
		target := m.mentionIndex.Candidates[match.Candidate]
		inserted = mentions.InsertText(target.Path, target.Kind == mentions.KindDirectory)
	}

	token := current
	old := m.textarea.Value()
	if token.Start < 0 || token.End < token.Start || token.End > len(old) || old[token.Start:token.End] == "" {
		return false, nil
	}
	suffix := old[token.End:]
	separator := " "
	if suffix != "" {
		if r, _ := utf8.DecodeRuneInString(suffix); unicode.IsSpace(r) {
			separator = ""
		}
	}
	newValue := old[:token.Start] + inserted + separator + suffix
	m.textarea.SetValue(newValue)
	m.moveTextareaCursorToByteOffset(token.Start + len(inserted) + len(separator))
	m.updateTextareaHeight()
	m.hideMentionPopup()
	return true, nil
}

func (m *Model) renderMentionPopup() string {
	if !m.mentionPopup.IsVisible() {
		return ""
	}
	p := &m.mentionPopup
	panel := composer.MentionPanel{
		Selected: p.selected,
		// Keep the selection marker on fresh qualified model rows even while
		// files and agents are still loading.
		Searching: p.searching && !p.modelsActionable(),
		Empty:     p.emptyMessage(),
	}
	if count := p.selectableCount(); count > 0 {
		panel.Rows = make([]composer.MentionRow, count)
		for i := range count {
			panel.Rows[i] = m.mentionRow(i)
		}
	}
	return panel.View(m.styles.Theme(), m.width)
}

// mentionRow converts one popup selection slot into a presentable row.
func (m *Model) mentionRow(index int) composer.MentionRow {
	p := &m.mentionPopup
	kind, offset := p.layout().RowAt(index)
	switch kind {
	case composer.MentionAgent:
		return composer.MentionRow{Label: mentions.DisplayAgentMention(p.agentMatches[offset].name), Detail: "agent"}
	case composer.MentionModel:
		if offset < 0 || offset >= len(p.modelRows) {
			return composer.MentionRow{}
		}
		detail, tail := composer.MentionDetail(p.modelRows[offset])
		return composer.MentionRow{Label: p.modelRows[offset].Label, Detail: detail, Tail: tail, Highlight: composer.MentionHighlightQuery(p.modelToken)}
	}
	fileSelection := offset
	if m.mentionIndex == nil || fileSelection < 0 || fileSelection >= len(p.matches) {
		return composer.MentionRow{}
	}
	match := p.matches[fileSelection]
	if match.Candidate < 0 || match.Candidate >= len(m.mentionIndex.Candidates) {
		return composer.MentionRow{}
	}
	candidate := m.mentionIndex.Candidates[match.Candidate]
	displayPath := candidate.Path
	if candidate.Kind == mentions.KindDirectory && !strings.HasSuffix(displayPath, "/") {
		displayPath += "/"
	}
	_, fileQuery, _ := routeMentionQuery(p.matchesToken)
	return composer.MentionRow{
		Label:     displayPath,
		Path:      true,
		Positions: composer.ContiguousPositions(displayPath, fileQuery),
	}
}

// emptyMessage is the muted popup line shown when no row is selectable.
func (p *mentionPopupModel) emptyMessage() string {
	return composer.MentionEmptyState{
		ModelOwned: p.modelOwned, EffortStep: p.modelToken.EffortStep,
		AgentsOnly: p.mode == mentionQueryAgentsOnly, Searching: p.searching,
		EmptyQuery: p.token.Query == "", Indexing: p.indexing,
		AgentErr: p.agentErr, IndexErr: p.err,
	}.Message()
}

func (m *Model) resetMentionsForRoot(dir string) {
	if !m.mentionEnabled {
		return
	}
	if m.mentionBuildCancel != nil {
		m.mentionBuildCancel()
		m.mentionBuildCancel = nil
	}
	if m.mentionQueryCancel != nil {
		m.mentionQueryCancel()
		m.mentionQueryCancel = nil
	}
	m.hideMentionPopup()
	m.mentionIndex = nil
	m.mentionRoot = ""
	m.mentionPopup.indexing = false
	root, err := mentionAbsoluteRoot(dir)
	if err != nil {
		m.mentionPopup.err = err
		return
	}
	m.mentionRoot = root
}

func (m *Model) eagerMentionContext(content string) (string, []string) {
	if m == nil || !m.mentionEnabled {
		return "", nil
	}
	root := m.mentionRoot
	if root == "" {
		root = m.effectiveWorkingDir()
	}
	var allowed func(string) bool
	if m.toolMgr != nil {
		allowed = m.toolMgr.IsReadPathApproved
	}
	ctx, cancel := context.WithTimeout(m.rootContext(), time.Second)
	defer cancel()
	attachments := mentions.LoadEagerAttachments(ctx, root, content, allowed)
	labels := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		labels = append(labels, attachment.Path)
	}
	return mentions.FormatEagerAttachments(attachments), labels
}
