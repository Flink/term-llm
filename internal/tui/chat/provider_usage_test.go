package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

func runProviderUsageCommand(t *testing.T, m *Model) *Model {
	t.Helper()
	m.setTextareaValue("/usage")
	updated, cmd := m.ExecuteCommand("/usage")
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("/usage returned no command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("/usage command = %T, want non-empty tea.BatchMsg", msg)
	}
	updated, _ = m.Update(batch[0]())
	return updated.(*Model)
}

func TestCmdProviderUsageOpensDialogForCurrentProvider(t *testing.T) {
	for _, test := range []struct {
		provider string
		report   providerusage.Report
		want     []string
	}{
		{"chatgpt", providerusage.Report{Provider: "chatgpt", Plan: "pro", Limits: []providerusage.Limit{{
			ID: "codex", Name: "Codex", PrimaryWindow: &providerusage.Window{UsedPercent: 31, DurationMinutes: 300},
		}}}, []string{"ChatGPT Codex", "Pro plan", "5 hour window\n  ███████░░░░░░░░░░░░░░░  31% used"}},
		{"agy-bin", providerusage.Report{Provider: "antigravity", Source: "Live from agy", Limits: []providerusage.Limit{{
			Name: "Gemini Models", PrimaryWindow: &providerusage.Window{Label: "5 hour window", UsedPercent: 40},
		}}}, []string{"Antigravity", "Live from agy", "Gemini Models", "40% used"}},
	} {
		t.Run(test.provider, func(t *testing.T) {
			m := newCmdTestModel(&mockStore{})
			m.providerKey = test.provider
			m.SetProviderUsageFetcher(func(_ context.Context, provider string) (*providerusage.Report, error) {
				if provider != test.provider {
					t.Fatalf("provider = %q, want %q", provider, test.provider)
				}
				return &test.report, nil
			})
			m = runProviderUsageCommand(t, m)
			if !m.dialog.IsOpen() || m.dialog.Type() != DialogContent {
				t.Fatalf("usage dialog not open: open=%v type=%v", m.dialog.IsOpen(), m.dialog.Type())
			}
			content := strings.Join(m.dialog.contentLines, "\n")
			for _, want := range test.want {
				if !strings.Contains(content, want) {
					t.Fatalf("dialog missing %q:\n%s", want, content)
				}
			}
		})
	}
}

func TestCmdProviderUsageReportsFetchErrors(t *testing.T) {
	m := newCmdTestModel(&mockStore{})
	m.providerKey = "openai"
	m.SetProviderUsageFetcher(func(context.Context, string) (*providerusage.Report, error) {
		return nil, errors.New(`live usage is not supported for provider "openai"`)
	})

	m = runProviderUsageCommand(t, m)
	if m.dialog.IsOpen() || !strings.Contains(m.footerMessage, `Unable to fetch openai usage: live usage is not supported`) {
		t.Fatalf("footer = %q", m.footerMessage)
	}
}

func TestCmdProviderUsageWithoutFetcher(t *testing.T) {
	m := newCmdTestModel(&mockStore{})
	m.providerKey = "chatgpt"
	m.setTextareaValue("/usage")
	updated, cmd := m.ExecuteCommand("/usage")
	m = updated.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if !strings.Contains(m.footerMessage, "not available") || m.textarea.Value() != "/usage" {
		t.Fatalf("footer = %q draft = %q", m.footerMessage, m.textarea.Value())
	}
}
