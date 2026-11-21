package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/subagentview"
	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/tools/agentrun"
)

func resolveDurableStatus(ctx context.Context, store session.AgentRunStore, parent string, run subagentview.CompletedRun) subagentview.CompletedRun {
	if store == nil || !agentrun.Active(run.SubagentStatus) || run.AgentID == "" || parent == "" || run.AgentID == parent || run.Error != "" {
		return run
	}
	row, err := store.GetAgentRun(ctx, run.AgentID)
	if err != nil || row.ID != run.AgentID || row.ParentSessionID != parent || (!agentrun.Active(row.Status) && !agentrun.Terminal(row.Status)) {
		return run
	}
	return run.WithSubagentStatus(row.Status)
}

// A later lifecycle receipt can close an open spawn even when the optional
// run store is unavailable. Stop at the first terminal observation: a later
// continue_agent generation doesn't reopen the historical spawn call.
func resolveTranscriptStatuses(messages []session.Message, sources []persistedSubagentSource) {
	byCall := make(map[string]*persistedSubagentSource)
	for i := range sources {
		s := &sources[i]
		if agentrun.Active(s.parsed.Status) {
			byCall[s.call.ID] = s
		}
	}
	if len(byCall) == 0 {
		return
	}
	open := make(map[string]*persistedSubagentSource)
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type != core.PartToolResult || part.ToolResult == nil {
				continue
			}
			observePersistedReceipt(part.ToolResult, byCall, open)
		}
	}
}

func observePersistedReceipt(r *core.ToolResult, byCall, open map[string]*persistedSubagentSource) {
	if s := byCall[r.ID]; s != nil {
		id := strings.TrimSpace(cmp.Or(s.parsed.AgentID, s.parsed.SessionID))
		if id != "" {
			open[id] = s
		}
		return
	}
	for _, receipt := range lifecycleReceipts(r) {
		if s := open[receipt.AgentID]; s != nil && agentrun.Terminal(receipt.Status) {
			s.parsed.Status = receipt.Status
			delete(open, receipt.AgentID)
		}
	}
}

type lifecycleReceipt struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

func lifecycleReceipts(r *core.ToolResult) []lifecycleReceipt {
	var rows []lifecycleReceipt
	switch r.Name {
	case tools.WaitAgentToolName, tools.ListAgentsToolName:
		if json.Unmarshal([]byte(r.Content), &rows) != nil {
			return nil
		}
	case tools.CancelAgentToolName, tools.ContinueAgentToolName:
		var row lifecycleReceipt
		if json.Unmarshal([]byte(r.Content), &row) == nil {
			rows = append(rows, row)
		}
	}
	return rows
}
