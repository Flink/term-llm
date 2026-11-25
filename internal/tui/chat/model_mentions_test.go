package chat

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mentions"
	"github.com/term-llm/term-llm/internal/tui/chat/composer"
	"github.com/term-llm/term-llm/internal/ui"
)

func modelMentionTestProviders() []mentions.ModelProvider {
	efforts := []string{"low", "medium", "high"}
	return []mentions.ModelProvider{
		{ID: "bedrock", Models: []mentions.ModelOption{
			{ID: "claude-opus-4-8", Name: "Opus 4.8"},
		}},
		{ID: "claude-bin", Models: []mentions.ModelOption{
			{ID: "opus", Name: "Opus 5.5", Alias: true, Efforts: efforts, DefaultEffort: "medium"},
			{ID: "claude-opus-4-8", Name: "Opus 4.8"},
			{ID: "opus-medium", Name: "Opus 5.5 medium"},
		}},
		{ID: "offline"},
	}
}

func newModelMentionTestModel(t *testing.T, value string) *Model {
	t.Helper()
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.modelMentionResolver = modelMentionTestProviders
	m.setTextareaValue(value)
	m.textarea.MoveToEnd()
	return m
}

func modelMentionLabels(m *Model) []string {
	labels := make([]string, 0, len(m.mentionPopup.modelRows))
	for _, row := range m.mentionPopup.modelRows {
		labels = append(labels, row.Label)
	}
	return labels
}

func acceptKey(t *testing.T, m *Model, code rune) {
	t.Helper()
	handled, _ := m.handleMentionPopupKey(tea.KeyPressMsg{Code: code})
	if !handled {
		t.Fatalf("key %q was not consumed by the mention popup", code)
	}
}

// TestScopedModelMentionCompletesEffortWithoutSideEffects walks the full
// `@m:opus` flow: the alias row keeps the token open, the effort row inserts
// plain text, and nothing is sent, attached, or switched.
func TestScopedModelMentionCompletesEffortWithoutSideEffects(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:opus")
	messages, providerKey, modelName := len(m.messages), m.providerKey, m.modelName

	if cmd := m.updateMentionQuery(); cmd != nil {
		t.Fatalf("scoped model query scheduled async work: %v", cmd)
	}
	if !m.mentionPopup.visible || !m.mentionPopup.modelOwned || m.mentionPopup.modelToken.Scoped != true {
		t.Fatalf("popup did not own the scoped token: %#v", m.mentionPopup)
	}
	if want := []string{"claude-bin:opus", "claude-bin:opus-medium", "claude-bin:claude-opus-4-8", "bedrock:claude-opus-4-8"}; !slices.Equal(modelMentionLabels(m), want) {
		t.Fatalf("rows = %v, want %v", modelMentionLabels(m), want)
	}

	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != "@model:claude-bin:opus#" {
		t.Fatalf("alias insertion = %q", got)
	}
	if !m.mentionPopup.visible || !m.mentionPopup.modelToken.EffortStep {
		t.Fatalf("effort step did not open: %#v", m.mentionPopup)
	}
	wantEfforts := []string{"claude-bin:opus", "claude-bin:opus-low", "claude-bin:opus-medium", "claude-bin:opus-high"}
	if !slices.Equal(modelMentionLabels(m), wantEfforts) {
		t.Fatalf("effort rows = %v, want %v", modelMentionLabels(m), wantEfforts)
	}
	if badge := m.mentionPopup.modelRows[2].Badge; badge != "default" {
		t.Fatalf("effort row badge = %q, want default", badge)
	}

	acceptKey(t, m, tea.KeyDown)
	acceptKey(t, m, tea.KeyDown)
	acceptKey(t, m, tea.KeyEnter)

	if got := m.textarea.Value(); got != "claude-bin:opus-medium " {
		t.Fatalf("final insertion = %q", got)
	}
	if m.mentionPopup.IsVisible() {
		t.Fatal("popup stayed open after a final model insertion")
	}
	if len(m.messages) != messages || len(m.files) != 0 || len(m.images) != 0 {
		t.Fatalf("model mention had side effects: messages=%d files=%#v images=%#v", len(m.messages), m.files, m.images)
	}
	if m.providerKey != providerKey || m.modelName != modelName {
		t.Fatalf("model mention switched models: %s:%s", m.providerKey, m.modelName)
	}
}

func TestModelMentionInsertionRespectsSuffixAndCursor(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:opus "+"tail")
	m.moveTextareaCursorToByteOffset(len("@m:opus"))
	if cmd := m.updateMentionQuery(); cmd != nil {
		t.Fatalf("scoped model query scheduled async work: %v", cmd)
	}
	if m.mentionPopup.modelToken.End != len("@m:opus") {
		t.Fatalf("model span = %#v, want it to end at the token boundary", m.mentionPopup.modelToken)
	}
	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != "@model:claude-bin:opus# tail" {
		t.Fatalf("continued token = %q", got)
	}
}

// TestModelMentionReplacesFullTokenAfterMultibyteText covers a caret inside the
// token after a multibyte prefix: matching stops at the caret but the whole
// token is replaced.
func TestModelMentionReplacesFullTokenAfterMultibyteText(t *testing.T) {
	const prefix = "猫🙂。 "
	m := newModelMentionTestModel(t, prefix+"@m:opus-stale")
	m.moveTextareaCursorToByteOffset(len(prefix + "@m:opus"))
	if cmd := m.updateMentionQuery(); cmd != nil {
		t.Fatalf("scoped model query scheduled async work: %v", cmd)
	}
	if token := m.mentionPopup.modelToken; token.Start != len(prefix) || token.End != len(prefix+"@m:opus-stale") || token.Query != "opus" {
		t.Fatalf("token = %#v", token)
	}
	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != prefix+"@model:claude-bin:opus#" {
		t.Fatalf("replacement = %q", got)
	}
}

func TestModelMentionTabEnterAndEscape(t *testing.T) {
	tabbed := newModelMentionTestModel(t, "@m:opus-medium")
	tabbed.updateMentionQuery()
	acceptKey(t, tabbed, tea.KeyTab)
	if got := tabbed.textarea.Value(); got != "claude-bin:opus-medium " || tabbed.mentionPopup.IsVisible() {
		t.Fatalf("tab insertion = %q, visible=%v", got, tabbed.mentionPopup.IsVisible())
	}

	entered := newModelMentionTestModel(t, "@m:opus-medium")
	entered.updateMentionQuery()
	acceptKey(t, entered, tea.KeyEnter)
	if got := entered.textarea.Value(); got != "claude-bin:opus-medium " {
		t.Fatalf("enter insertion = %q", got)
	}

	escaped := newModelMentionTestModel(t, "@m:opus")
	escaped.updateMentionQuery()
	acceptKey(t, escaped, tea.KeyEscape)
	if escaped.mentionPopup.IsVisible() || escaped.textarea.Value() != "@m:opus" {
		t.Fatalf("escape changed the draft: %q visible=%v", escaped.textarea.Value(), escaped.mentionPopup.IsVisible())
	}
}

func TestModelMentionEmptyResultsDoNotSendOrHideWrongly(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:zzz")
	m.width = 80
	m.updateMentionQuery()
	if len(m.mentionPopup.modelRows) != 0 || !m.mentionPopup.modelOwned {
		t.Fatalf("unexpected rows: %#v", m.mentionPopup.modelRows)
	}
	if popup := ui.StripANSI(m.renderMentionPopup()); !strings.Contains(popup, "no matching models") {
		t.Fatalf("empty model popup = %q", popup)
	}
	before := len(m.messages)
	acceptKey(t, m, tea.KeyEnter)
	if len(m.messages) != before || m.textarea.Value() != "@m:zzz" {
		t.Fatalf("empty model popup sent or edited: messages=%d value=%q", len(m.messages)-before, m.textarea.Value())
	}
	if m.mentionPopup.IsVisible() {
		t.Fatal("empty model popup stayed visible after enter")
	}

	effort := newModelMentionTestModel(t, "@m:claude-bin:opus#zzz")
	effort.updateMentionQuery()
	if popup := ui.StripANSI(effort.renderMentionPopup()); !strings.Contains(popup, "no matching effort choices") {
		t.Fatalf("empty effort popup = %q", popup)
	}
}

func TestScopedModelQueryDoesNotTouchIndexOrAgents(t *testing.T) {
	m := newModelMentionTestModel(t, "@model:op")
	m.mentionRoot = t.TempDir()
	capability := &fakeAgentMentionCapability{names: []string{"opus-helper"}, denied: map[string]error{}}
	m.agentMentionCapability = capability

	if cmd := m.updateMentionQuery(); cmd != nil {
		t.Fatalf("scoped model query scheduled async work: %v", cmd)
	}
	if m.mentionIndex != nil || m.mentionBuildCancel != nil {
		t.Fatal("scoped model query built or queried the project index")
	}
	if capability.listCalls != 0 {
		t.Fatalf("scoped model query consulted the agent catalog %d times", capability.listCalls)
	}
	if m.mentionPopup.indexing || m.mentionPopup.searching || len(m.mentionPopup.agentMatches) != 0 || len(m.mentionPopup.matches) != 0 {
		t.Fatalf("scoped model popup retained other sources: %#v", m.mentionPopup)
	}
}

func TestLooseModelMentionKeepsAgentAndFileSuggestions(t *testing.T) {
	m := newModelMentionTestModel(t, "@claude")
	m.mentionRoot = t.TempDir()
	m.mentionIndex = &mentions.Snapshot{Candidates: []mentions.Candidate{testMentionCandidate("cmd/claude.go")}}
	m.agentMentionCapability = &fakeAgentMentionCapability{names: []string{"claude-helper"}, denied: map[string]error{}}

	if cmd := m.updateMentionQuery(); cmd == nil {
		t.Fatal("loose query did not schedule agent and file search")
	}
	value := m.textarea.Value()
	token, ok := mentions.ActiveTokenAt(value, len(value))
	if !ok {
		t.Fatal("loose mention token not found")
	}
	handled, next := m.handleMentionMessage(mentionDebounceMsg{
		root: m.mentionRoot, generation: m.mentionIndexGeneration, request: m.mentionQueryRequest,
		token: token, cursor: len(value),
	})
	if !handled || next == nil {
		t.Fatal("debounced query was not handled")
	}
	if handled, _ := m.handleMentionMessage(next()); !handled {
		t.Fatal("match result was not handled")
	}
	p := &m.mentionPopup
	if p.modelOwned || p.modelToken.Scoped {
		t.Fatalf("loose token was treated as scoped: %#v", p.modelToken)
	}
	if len(p.agentMatches) != 1 || len(p.modelRows) == 0 || len(p.matches) != 1 {
		t.Fatalf("loose categories = agents %d models %d files %d", len(p.agentMatches), len(p.modelRows), len(p.matches))
	}
	if p.selected != 0 || p.selectableCount() != 1+len(p.modelRows)+1 {
		t.Fatalf("selection map = %d of %d", p.selected, p.selectableCount())
	}
	// Bare provider prefixes keep agent and file suggestions first; models are
	// appended so a file retains the default selection.
	if kind, _ := p.layout().RowAt(0); kind != composer.MentionAgent {
		t.Fatalf("first row kind = %v, want an agent", kind)
	}
	if kind, _ := p.layout().RowAt(1); kind != composer.MentionFile {
		t.Fatalf("second row kind = %v, want a file", kind)
	}

	acceptKey(t, m, tea.KeyDown)
	acceptKey(t, m, tea.KeyDown)
	if kind, offset := p.layout().RowAt(p.selected); kind != composer.MentionModel || p.modelRows[offset].Label != "claude-bin:opus" {
		t.Fatalf("model row at %d = %v, %d", p.selected, kind, offset)
	}
	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != "@model:claude-bin:opus#" {
		t.Fatalf("loose model insertion = %q", got)
	}
}

func TestScopedModelSelectionResetsOnlyWhenTokenChanges(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:opus")
	m.updateMentionQuery()
	acceptKey(t, m, tea.KeyDown)
	if m.mentionPopup.selected != 1 {
		t.Fatalf("selected = %d, want 1", m.mentionPopup.selected)
	}
	m.setTextareaValue("@m:opu")
	m.textarea.MoveToEnd()
	m.updateMentionQuery()
	if m.mentionPopup.selected != 0 {
		t.Fatalf("changed scoped query kept selection %d", m.mentionPopup.selected)
	}
	acceptKey(t, m, tea.KeyDown)
	m.updateMentionQuery()
	if m.mentionPopup.selected != 1 {
		t.Fatalf("identical scoped update reset selection to %d", m.mentionPopup.selected)
	}
}

func TestQualifiedModelRowSelectableWhileFilesLoad(t *testing.T) {
	m := newModelMentionTestModel(t, "@claude-bin:op")
	if cmd := m.updateMentionQuery(); cmd == nil {
		t.Fatal("qualified model query did not schedule the agent/file search")
	}
	if !m.mentionPopup.searching || !m.mentionPopup.modelOwned && !m.mentionPopup.modelSelectionReady() {
		t.Fatalf("fresh qualified row was locked: %#v", m.mentionPopup)
	}
	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != "@model:claude-bin:opus#" {
		t.Fatalf("qualified model insertion = %q", got)
	}
	if m.mentionQueryCancel != nil {
		t.Fatal("choosing a model left the previous debounce running")
	}

	blocked := newModelMentionTestModel(t, "@claude")
	blocked.updateMentionQuery()
	if blocked.mentionPopup.modelSelectionReady() {
		t.Fatal("unqualified query treated a model row as independently ready")
	}
	acceptKey(t, blocked, tea.KeyEnter)
	if blocked.textarea.Value() != "@claude" || !blocked.mentionPopup.IsVisible() {
		t.Fatalf("enter during pending unqualified search changed the draft: %q", blocked.textarea.Value())
	}
}

func TestScopedModelQueryCancelsPendingDebounce(t *testing.T) {
	m := newModelMentionTestModel(t, "@claude")
	m.updateMentionQuery()
	if m.mentionQueryCancel == nil {
		t.Fatal("test query did not schedule a debounce")
	}
	m.setTextareaValue("@m:opus")
	m.textarea.MoveToEnd()
	m.updateMentionQuery()
	if m.mentionQueryCancel != nil {
		t.Fatal("entering a scoped model query kept the previous debounce alive")
	}
	if !m.mentionPopup.modelOwned {
		t.Fatalf("popup did not own the scoped query: %#v", m.mentionPopup)
	}
}

func TestModelMentionSelectionRejectsStaleToken(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:opus")
	m.updateMentionQuery()
	m.setTextareaValue("@m:claude")
	before := m.textarea.Value()
	if accepted, _ := m.acceptMentionSelection(); accepted || m.textarea.Value() != before {
		t.Fatalf("stale model row was accepted: value=%q", m.textarea.Value())
	}
}

func TestModelMentionCatalogResolvesOnce(t *testing.T) {
	calls := 0
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.modelMentionResolver = func() []mentions.ModelProvider {
		calls++
		return modelMentionTestProviders()
	}
	for _, value := range []string{"@m:o", "@model:opus", "@claude"} {
		m.setTextareaValue(value)
		m.textarea.MoveToEnd()
		m.updateMentionQuery()
	}
	if calls != 1 {
		t.Fatalf("catalog resolved %d times, want once per session", calls)
	}
}

func TestModelMentionPopupRendersLabelBadgeAndDescription(t *testing.T) {
	m := newModelMentionTestModel(t, "@m:opus")
	m.width = 100
	m.updateMentionQuery()
	content := ui.StripANSI(m.renderMentionPopup())
	for _, want := range []string{"claude-bin:opus", "latest", "effort: low, medium, high"} {
		if !strings.Contains(content, want) {
			t.Fatalf("popup %q missing %q", content, want)
		}
	}
	if strings.Contains(content, "no matching models") || strings.Contains(content, "indexing project files") {
		t.Fatalf("popup shows an empty state with rows: %q", content)
	}
}

func TestSubmittedScopedModelMentionIsNotAnAttachment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "m:opus"), []byte("model-named-body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestChatModel(false)
	m.mentionRoot = root
	content := "use @m:opus"
	_, _ = m.sendMessage(content)
	last := m.messages[len(m.messages)-1]
	providerText := core.MessageText(last.ToLLMMessage())
	if last.TextContent != content || strings.Contains(providerText, "model-named-body") {
		t.Fatalf("scoped model token became an attachment: visible=%q provider=%q", last.TextContent, providerText)
	}

	quoted := newTestChatModel(false)
	quoted.mentionRoot = root
	_, _ = quoted.sendMessage(`use @"m:opus"`)
	quotedLast := quoted.messages[len(quoted.messages)-1]
	if providerText := core.MessageText(quotedLast.ToLLMMessage()); !strings.Contains(providerText, "model-named-body") {
		t.Fatalf("quoted file with a model namespace was not attached: %q", providerText)
	}
}

func TestModelMentionCatalogRefreshesOnlyForFreshMentions(t *testing.T) {
	calls := 0
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.modelMentionResolver = func() []mentions.ModelProvider {
		calls++
		return modelMentionTestProviders()
	}
	query := func(value string) {
		m.setTextareaValue(value)
		m.textarea.MoveToEnd()
		m.updateMentionQuery()
	}
	query("@m:o")
	if calls != 1 {
		t.Fatalf("first mention resolved the catalog %d times", calls)
	}
	query("@m:op")
	query("@m:opu")
	if calls != 1 {
		t.Fatalf("keystrokes inside one mention refreshed the catalog: %d", calls)
	}
	m.hideMentionPopup()
	query("@m:opus")
	if calls != 2 {
		t.Fatalf("a fresh mention reused the stale catalog: %d", calls)
	}
}

func TestModelMentionCatalogInvalidatedByModelRefresh(t *testing.T) {
	calls := 0
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.modelMentionResolver = func() []mentions.ModelProvider {
		calls++
		return modelMentionTestProviders()
	}
	m.setTextareaValue("@m:opus")
	m.textarea.MoveToEnd()
	m.updateMentionQuery()
	if calls != 1 {
		t.Fatalf("catalog resolutions = %d", calls)
	}
	m.applyChatGPTModelsLoaded(chatGPTModelsLoadedMsg{})
	m.updateMentionQuery()
	if calls != 2 {
		t.Fatalf("model catalog refresh did not invalidate the mention snapshot: %d", calls)
	}
}

// TestScopedModelMentionWorksWithoutOtherSources covers a project-less or
// failed-index session: explicit @model:/@m: rows must still render and insert.
func TestScopedModelMentionWorksWithoutOtherSources(t *testing.T) {
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.modelMentionResolver = modelMentionTestProviders
	m.mentionPopup.err = errors.New("project directory is unavailable")
	m.width = 80
	m.setTextareaValue("@m:opus")
	m.textarea.MoveToEnd()

	if cmd := m.updateMentionQuery(); cmd != nil {
		t.Fatalf("scoped query scheduled file or agent work: %v", cmd)
	}
	if !m.mentionPopup.modelOwned || len(m.mentionPopup.modelRows) == 0 {
		t.Fatalf("scoped rows missing without other sources: %#v", m.mentionPopup)
	}
	if m.mentionIndex != nil || m.mentionBuildCancel != nil || m.mentionPopup.indexing {
		t.Fatal("scoped query depended on the project index")
	}
	content := ui.StripANSI(m.renderMentionPopup())
	if !strings.Contains(content, "claude-bin:opus") || strings.Contains(content, "file index unavailable") {
		t.Fatalf("scoped popup without other sources = %q", content)
	}
	acceptKey(t, m, tea.KeyEnter)
	if got := m.textarea.Value(); got != "@model:claude-bin:opus#" {
		t.Fatalf("scoped insertion = %q", got)
	}

	bare := newTestChatModel(false)
	bare.mentionEnabled = true
	bare.modelMentionResolver = func() []mentions.ModelProvider { return nil }
	bare.width = 80
	bare.setTextareaValue("@model:anything")
	bare.textarea.MoveToEnd()
	bare.updateMentionQuery()
	if !bare.mentionPopup.modelOwned {
		t.Fatal("scoped token without any provider catalog lost its namespace")
	}
	if popup := ui.StripANSI(bare.renderMentionPopup()); !strings.Contains(popup, "no matching models") {
		t.Fatalf("empty catalog popup = %q", popup)
	}
}

func TestMentionPopupKeepsFileTruncationHint(t *testing.T) {
	m := newTestChatModel(false)
	m.mentionEnabled = true
	m.width = 30
	prepareMentionPopup(m, t.TempDir(), "review @very", mentions.Candidate{Path: "very/deep/project/path/internal/session/types.go"})
	content := ui.StripANSI(m.renderMentionPopup())
	if !strings.Contains(content, "…") || !strings.Contains(content, "types.go") {
		t.Fatalf("truncated file row lost its hint or basename: %q", content)
	}
}

// completeMentionSearch resolves the debounced file and agent query for the
// current token, as the Bubble Tea loop would.
func completeMentionSearch(t *testing.T, m *Model) {
	t.Helper()
	value := m.textarea.Value()
	token, ok := mentions.ActiveTokenAt(value, len(value))
	if !ok {
		t.Fatal("mention token not found")
	}
	handled, next := m.handleMentionMessage(mentionDebounceMsg{
		root: m.mentionRoot, generation: m.mentionIndexGeneration, request: m.mentionQueryRequest,
		token: token, cursor: len(value),
	})
	if !handled || next == nil {
		t.Fatal("debounced query was not handled")
	}
	if handled, _ := m.handleMentionMessage(next()); !handled {
		t.Fatal("match result was not handled")
	}
}

func TestQualifiedModelRowsSurviveSearchWhileStaleRowsRemain(t *testing.T) {
	m := newModelMentionTestModel(t, "@claude")
	m.mentionRoot = t.TempDir()
	m.mentionIndex = &mentions.Snapshot{Candidates: []mentions.Candidate{testMentionCandidate("cmd/claude.go")}}
	m.agentMentionCapability = &fakeAgentMentionCapability{names: []string{"claude-helper"}, denied: map[string]error{}}
	m.updateMentionQuery()
	completeMentionSearch(t, m)
	if len(m.mentionPopup.agentMatches) == 0 || len(m.mentionPopup.matches) == 0 {
		t.Fatalf("expected agent and file rows before switching: %#v", m.mentionPopup)
	}

	m.setTextareaValue("@claude-bin:o")
	m.textarea.MoveToEnd()
	if cmd := m.updateMentionQuery(); cmd == nil {
		t.Fatal("qualified query did not schedule the pending search")
	}
	if !m.mentionPopup.searching || !m.mentionPopup.modelsActionable() {
		t.Fatalf("qualified rows are not usable while searching: %#v", m.mentionPopup)
	}
	if len(m.mentionPopup.agentMatches) == 0 || len(m.mentionPopup.matches) == 0 {
		t.Fatal("retained rows were dropped before the new search resolved")
	}
	models := len(m.mentionPopup.modelRows)
	if models < 2 {
		t.Fatalf("model rows = %d, want at least two", models)
	}
	for step := range models * 2 {
		acceptKey(t, m, tea.KeyDown)
		if m.mentionPopup.selected >= models {
			t.Fatalf("step %d cycled into pending agent/file rows: selected=%d models=%d", step, m.mentionPopup.selected, models)
		}
	}

	// An index refresh preserves the selected fresh model row.
	m.mentionPopup.selected = 1
	if handled, _ := m.handleMentionMessage(mentionIndexReadyMsg{
		root: m.mentionRoot, generation: m.mentionIndexGeneration, snapshot: &mentions.Snapshot{BuiltAt: time.Now()},
	}); !handled || m.mentionPopup.selected != 1 {
		t.Fatalf("index arrival moved the selected model row: handled=%v selected=%d", handled, m.mentionPopup.selected)
	}

	// Agent and file results preserve it as well.
	value := m.textarea.Value()
	token, ok := mentions.ActiveTokenAt(value, len(value))
	if !ok {
		t.Fatal("qualified token not found")
	}
	handled, _ := m.handleMentionMessage(mentionMatchesMsg{
		root: m.mentionRoot, generation: m.mentionIndexGeneration, request: m.mentionQueryRequest,
		token: token, cursor: len(value), mode: mentionQueryGeneral,
		matches: []mentions.Match{}, agentMatches: []agentMentionMatch{},
	})
	if !handled || m.mentionPopup.selected != 1 {
		t.Fatalf("search result moved the selected model row: handled=%v selected=%d", handled, m.mentionPopup.selected)
	}

	// A changed qualified token starts over; an identical update does not.
	m.setTextareaValue("@claude-bin:op")
	m.textarea.MoveToEnd()
	m.updateMentionQuery()
	if m.mentionPopup.selected != 0 {
		t.Fatalf("changed qualified token kept selection %d", m.mentionPopup.selected)
	}
	m.mentionPopup.selected = 1
	m.updateMentionQuery()
	if m.mentionPopup.selected != 1 {
		t.Fatalf("identical qualified update reset selection to %d", m.mentionPopup.selected)
	}
}
