package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

// SetProviderUsageFetcher installs the live subscription-limit fetcher used by /usage.
func (m *Model) SetProviderUsageFetcher(fetch func(ctx context.Context, provider string) (*providerusage.Report, error)) {
	m.providerUsageFetcher = fetch
}

type providerUsageDoneMsg struct {
	provider string
	report   *providerusage.Report
	err      error
}

func (m *Model) cmdProviderUsage(args []string) (tea.Model, tea.Cmd) {
	if len(args) != 0 {
		return m.showFooterError("Usage: /usage")
	}
	provider, _ := m.currentProviderAndModel()
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return m.showFooterError("Unable to determine the current provider.")
	}
	fetch := m.providerUsageFetcher
	if fetch == nil {
		return m.showFooterError("Live provider usage is not available in this session.")
	}
	m.setTextareaValue("")
	parentCtx := m.rootContext()
	return m.showFooterMutedWithCmd("Fetching "+provider+" usage…", func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
		defer cancel()
		report, err := fetch(ctx, provider)
		return providerUsageDoneMsg{provider: provider, report: report, err: err}
	})
}

func (m *Model) handleProviderUsageDone(msg providerUsageDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.showFooterError(fmt.Sprintf("Unable to fetch %s usage: %v", msg.provider, msg.err))
	}
	width := 0
	if m.dialog != nil {
		width = m.dialog.contentWidth() - 4
	}
	content := providerusage.FormatWithOptions(msg.report, time.Now(), providerusage.FormatOptions{Width: width})
	if strings.TrimSpace(content) == "" {
		return m.showFooterError("Provider returned an empty usage report.")
	}
	m.clearFooterMessage()
	m.dialog.ShowContent("Provider Usage", content)
	m.scrollToBottom = true
	return m, nil
}
