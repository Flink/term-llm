package chat

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/ui"
)

func TestEmojiPopupTrigger(t *testing.T) {
	for _, tc := range []struct {
		name, draft string
		visible     bool
	}{
		{"start of draft", ":ro", true},
		{"after a space", "Launch :ro", true},
		{"after Unicode whitespace", "Launch\u00a0:ro", true},
		{"after a newline", "Launch\n:ro", true},
		{"after emoji", "go🚀:ro", true},
		{"after variation selector", "🏵️:ro", true},
		{"after flag", "🇵🇱:ro", true},
		{"after keycap", "1️⃣:ro", true},
		{"after ZWJ sequence", "🐻‍❄️:ro", true},
		{"after modifier", "👍🏾:ro", true},
		{"plus alias", ":+1", true},
		{"minus alias", ":-1", true},
		{"ordinary number", "101:ro", false},
		{"URL port", "https://example.com:80", false},
		{"URL path", "https://example.com/:ro", false},
		{"only a colon", ":", false},
		{"only one shortcode character", ":r", false},
		{"non-ASCII letter in shortcode", ":rö", false},
		{"colon attached to a word", "launch:rocket", false},
		{"colon attached to punctuation", "(:rocket", false},
		{"cursor after a completed shortcode", ":rocket:", false},
		{"cursor beyond the shortcode", ":rocket now", false},
		{"no catalog matches", ":not_an_emoji_name", false},
		{"direct shell input", "!echo :rocket", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := typeComposerKeys(newTestChatModel(false), tc.draft)
			view := ui.StripANSI(m.View().Content)
			if strings.Contains(view, "enter/tab insert") != tc.visible {
				t.Fatalf("emoji popup visibility for %q: want %t\n%s", tc.draft, tc.visible, view)
			}
		})
	}
}

func TestEmojiPopupCompletesWithoutSending(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      rune
		alt      bool
		steering bool
		pending  bool
	}{
		{name: "inline Tab", key: tea.KeyTab},
		{name: "alternate screen Enter", key: tea.KeyEnter, alt: true},
		{name: "streaming Enter", key: tea.KeyEnter, steering: true},
		{name: "Enter during inline scrollback acknowledgement", key: tea.KeyEnter, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestChatModel(tc.alt)
			m.streaming = tc.steering
			m.inlineCompletionPending = tc.pending
			prefix := strings.Repeat("Café ", 22) + "\n雪: not a shortcode\nNorth "
			suffix := ", then home 🏠\nDone"
			m.setTextareaValue(prefix + suffix)
			m.moveTextareaCursorToByteOffset(len(prefix))
			m = typeComposerKeys(m, ":polar_be")
			if view := ui.StripANSI(m.View().Content); !strings.Contains(view, "🐻‍❄️") {
				t.Fatalf("emoji popup is missing in composer:\n%s", view)
			}

			updated, cmd := m.Update(tea.KeyPressMsg{Code: tc.key})
			m = updated.(*Model)
			if want := prefix + "🐻‍❄️" + suffix; m.textarea.Value() != want {
				t.Fatalf("completed draft = %q, want %q", m.textarea.Value(), want)
			}
			if cmd != nil || len(m.messages) != 0 || len(m.pendingSteering) != 0 || m.inlineCompletionPendingKey != nil || m.streaming != tc.steering {
				t.Fatal("accepting an emoji triggered submission instead of only editing the draft")
			}
			if view := ui.StripANSI(m.View().Content); strings.Contains(view, "enter/tab insert") {
				t.Fatalf("popup did not close after accepting:\n%s", view)
			}
			m = typeComposerKeys(m, ":rocket")
			updated, cmd = m.Update(tea.KeyPressMsg{Code: tc.key})
			m = updated.(*Model)
			if cmd != nil || len(m.messages) != 0 || len(m.pendingSteering) != 0 || m.inlineCompletionPendingKey != nil || m.streaming != tc.steering {
				t.Fatal("accepting a chained emoji triggered submission")
			}
			m = typeComposerKeys(m, "!")
			if want := prefix + "🐻‍❄️🚀!" + suffix; m.textarea.Value() != want {
				t.Fatalf("typing after completion produced %q, want %q", m.textarea.Value(), want)
			}
		})
	}
}

func TestEmojiPopupNavigationAndEscape(t *testing.T) {
	m := typeComposerKeys(newTestChatModel(false), ":sm")
	first := selectedEmojiRow(m)
	if len(first) < 2 {
		t.Fatalf("missing initially selected emoji row: %q", first)
	}
	for _, tc := range []struct {
		key       tea.KeyPressMsg
		wantFirst bool
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, false},
		{tea.KeyPressMsg{Code: tea.KeyUp}, true},
		{tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}, false},
		{tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, true},
	} {
		_, _ = m.Update(tc.key)
		row := selectedEmojiRow(m)
		if len(row) < 2 || slices.Equal(row, first) != tc.wantFirst {
			t.Fatalf("%s selected %q (initial selection %q, want initial: %t)", tc.key, row, first, tc.wantFirst)
		}
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	selected := selectedEmojiRow(m)
	if len(selected) < 2 {
		t.Fatal("missing emoji row after navigating")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.textarea.Value(); got != selected[0] {
		t.Fatalf("Tab inserted %q, want highlighted emoji %q", got, selected[0])
	}

	m = typeComposerKeys(newTestChatModel(false), ":rocket")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.textarea.Value() != ":rocket" || len(selectedEmojiRow(m)) != 0 {
		t.Fatal("Escape did not dismiss completion without changing the draft")
	}
}

func TestEmojiPopupFollowsCursorInExistingShortcode(t *testing.T) {
	m := newTestChatModel(false)
	m.setTextareaValue("Café :rocket: soon")
	m.moveTextareaCursorToByteOffset(len("Café :rocket"))
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if row := selectedEmojiRow(m); len(row) == 0 || row[0] != "🚀" {
		t.Fatalf("moving into an existing shortcode selected %q, want rocket", row)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.textarea.Value(); got != "Café 🚀 soon" {
		t.Fatalf("completing inside a shortcode produced %q", got)
	}
}

func TestEmojiPopupRespectsTextareaCharacterLimit(t *testing.T) {
	const draft = ":fam, then home"
	const glyph = "👨‍👩‍👦"
	for _, tc := range []struct {
		name string
		room int
		want string
	}{
		{"no room for the whole glyph", 0, draft},
		{"room for the whole glyph", 1, glyph + ", then home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestChatModel(false)
			m.textarea.CharLimit = len(draft) + tc.room
			m.setTextareaValue(draft)
			m.moveTextareaCursorToByteOffset(len(":fa"))
			_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			if row := selectedEmojiRow(m); len(row) == 0 || row[0] != glyph {
				t.Fatalf("selected emoji = %q, want %q", row, glyph)
			}
			_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if got := m.textarea.Value(); got != tc.want || len(m.messages) != 0 || m.streaming {
				t.Fatalf("Enter left draft %q, want %q without submission", got, tc.want)
			}
		})
	}
}

func selectedEmojiRow(m *Model) []string {
	for line := range strings.SplitSeq(ui.StripANSI(m.View().Content), "\n") {
		if _, row, ok := strings.Cut(line, "› "); ok {
			return strings.Fields(row)
		}
	}
	return nil
}

func TestEmojiAltScreenPopupClearsUnderlyingRows(t *testing.T) {
	m := typeComposerKeys(newTestChatModel(true), ":rosette")
	base := strings.Repeat(strings.Repeat("x", m.width-1)+"│\n", m.height)
	frame := ui.StripANSI(m.overlayAltScreenPanels(base, footerLayout{height: 2}))
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "🏵️") {
			if !strings.HasSuffix(strings.TrimSpace(line), "│") || strings.Contains(line, "x") {
				t.Fatalf("missing frame or underlying content showing through: %q", line)
			}
			return
		}
	}
	t.Fatalf("missing rosette preview: %q", frame)
}

func TestEmojiPopupBlocksScriptClicks(t *testing.T) {
	m := newTestChatModel(true)
	if m.scriptClickBlocked() {
		t.Fatal("idle chat should allow script clicks")
	}
	m = typeComposerKeys(m, ":rocket")
	if !m.scriptClickBlocked() {
		t.Fatal("active emoji popup should block underlying script clicks")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.scriptClickBlocked() {
		t.Fatal("dismissed emoji popup should unblock script clicks")
	}
}
