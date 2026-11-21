package chat

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/tools/agentrun"
)

type spawnRowKey struct{ parent, child string }
type spawnRowObservedMsg struct {
	spawnRowKey
	status string
}

// Observe a detached child independently of wait_agent: a multi-child wait
// need not return when the first sibling finishes. Only Update owns the rows.
func (m *Model) observeSpawnRow(child string) tea.Cmd {
	key := spawnRowKey{parent: m.SessionID(), child: child}
	if key.parent == "" || child == "" || m.observingSpawnRows[key] {
		return nil
	}
	if m.observingSpawnRows == nil {
		m.observingSpawnRows = make(map[spawnRowKey]bool)
	}
	m.observingSpawnRows[key] = true
	ctx, store := m.rootContext(), session.AsAgentRunStore(m.store)
	return m.passiveCommand(func() tea.Msg {
		status, err := tools.ObserveAgentStatus(ctx, store, key.parent, child)
		if err != nil {
			status = "" // Unavailable is not evidence of either success or failure.
		} else if agentrun.Active(status) {
			// A remote run has no local completion channel. Rate-limit its next
			// snapshot, and only request it if the row is still visible.
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				status = ""
			case <-timer.C:
			}
		}
		return spawnRowObservedMsg{key, status}
	})
}

func (m *Model) observeActiveSpawnRows() tea.Cmd {
	var cmds []tea.Cmd
	if m.tracker != nil {
		for _, id := range m.tracker.ActiveSpawnAgentIDs() {
			cmds = append(cmds, m.observeSpawnRow(id))
		}
	}
	for _, run := range m.persistedSubagents {
		if agentrun.Active(run.SubagentStatus) {
			cmds = append(cmds, m.observeSpawnRow(run.AgentID))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) applySpawnRowObservation(msg spawnRowObservedMsg) tea.Cmd {
	delete(m.observingSpawnRows, msg.spawnRowKey)
	if msg.parent != m.SessionID() || msg.status == "" {
		return nil
	}
	if agentrun.Active(msg.status) {
		// The child is on another host. Poll only while its row still needs it.
		return m.observeActiveSpawnRows()
	}
	if m.tracker != nil && m.tracker.ResolveSpawnAgent(msg.child, msg.status) {
		m.rerenderCompletedStreamFromTracker()
	}
	for callID, run := range m.persistedSubagents {
		if run.AgentID == msg.child && agentrun.Active(run.SubagentStatus) {
			m.persistedSubagents[callID] = run.WithSubagentStatus(msg.status)
			m.invalidateHistoryCache()
		}
	}
	return nil
}
