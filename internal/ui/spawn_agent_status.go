package ui

import (
	"encoding/json"

	"github.com/term-llm/term-llm/internal/tools"
	"github.com/term-llm/term-llm/internal/tools/agentrun"
)

func knownSubagentStatus(status string) bool {
	return agentrun.Active(status) || agentrun.Terminal(status)
}

func toolStatusCircle(name string, call ToolStatus, child string) string {
	switch call {
	case ToolError:
		return ErrorCircle()
	case ToolPending:
		if name == tools.SpawnAgentToolName {
			return WorkingCircle()
		}
		return PendingCircle()
	}
	if name == tools.SpawnAgentToolName {
		switch {
		case agentrun.Active(child):
			return WorkingCircle()
		case child != "completed" && agentrun.Terminal(child):
			return ErrorCircle()
		}
	}
	return SuccessCircle()
}

func (s *Segment) activeReturnedSpawn() bool {
	return s.ToolName == tools.SpawnAgentToolName && s.ToolStatus == ToolSuccess && agentrun.Active(s.SubagentStatus)
}

// Only live flushes need this barrier. End-of-stream FlushAllRemaining must
// still emit the last observed state, even when the child outlives the turn.
func (s *Segment) blocksLiveFlush() bool {
	return s.Type == SegmentTool && (s.ToolStatus == ToolPending || s.activeReturnedSpawn())
}

// agentRowReceipt is the entire lifecycle payload needed for row presentation.
// Decode only these fields so large child output is neither decoded into a
// string field nor retained by the queued UI event.
type agentRowReceipt struct {
	ID     string `json:"agent_id"`
	Status string `json:"status"`
}

func parseAgentReceipt(output string) (agentRowReceipt, bool) {
	var row struct {
		agentRowReceipt
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(output), &row) != nil || !knownSubagentStatus(row.Status) {
		return agentRowReceipt{}, false
	}
	if row.ID == "" {
		row.ID = row.SessionID
	}
	return row.agentRowReceipt, row.ID != ""
}

func parseAgentRoster(output string) []agentRowReceipt {
	var rows []agentRowReceipt
	if json.Unmarshal([]byte(output), &rows) != nil {
		return nil
	}
	keep := rows[:0]
	for _, r := range rows {
		if r.ID != "" && knownSubagentStatus(r.Status) {
			keep = append(keep, r)
		}
	}
	return keep
}

func compactAgentToolOutput(name, output string) string {
	var receipt any
	switch name {
	case tools.SpawnAgentToolName, tools.CancelAgentToolName, tools.ContinueAgentToolName:
		row, ok := parseAgentReceipt(output)
		if !ok {
			return ""
		}
		receipt = row
	case tools.ListAgentsToolName, tools.WaitAgentToolName:
		rows := parseAgentRoster(output)
		if len(rows) == 0 {
			return ""
		}
		receipt = rows
	default:
		return ""
	}
	data, _ := json.Marshal(receipt) // only strings and slices of strings
	return string(data)
}

// ApplySpawnAgentReceipt attaches recognized child lifecycle metadata to a
// spawn row without changing ToolStatus. Historical segments use the same
// RenderToolSegment path as live rows after applying their matching result.
func ApplySpawnAgentReceipt(seg *Segment, output string) {
	if seg == nil || seg.ToolName != tools.SpawnAgentToolName {
		return
	}
	r, ok := parseAgentReceipt(output)
	if !ok {
		return
	}
	seg.SubagentID = r.ID
	seg.SubagentStatus = r.Status
}

// ActiveSpawnAgentIDs returns the IDs of returned, still-active spawn rows in
// presentation order. Pending tool calls have not returned an ID yet.
func (t *ToolTracker) ActiveSpawnAgentIDs() []string {
	var ids []string
	for i := range t.Segments {
		s := &t.Segments[i]
		if s.activeReturnedSpawn() && s.SubagentID != "" {
			ids = append(ids, s.SubagentID)
		}
	}
	return ids
}

// ResolveSpawnAgent updates a returned, active spawn row and invalidates its
// rendering. Once terminal, late snapshots cannot reopen the original spawn.
func (t *ToolTracker) ResolveSpawnAgent(childID, status string) bool {
	if childID == "" || !knownSubagentStatus(status) {
		return false
	}
	for i := range t.Segments {
		s := &t.Segments[i]
		if s.SubagentID == childID && s.activeReturnedSpawn() && s.SubagentStatus != status {
			s.SubagentStatus = status
			t.Version++
			return true
		}
	}
	return false
}

func (t *ToolTracker) applyAgentToolResult(seg *Segment, output string) {
	switch seg.ToolName {
	case tools.SpawnAgentToolName:
		ApplySpawnAgentReceipt(seg, output)
	case tools.CancelAgentToolName, tools.ContinueAgentToolName:
		r, ok := parseAgentReceipt(output)
		if ok {
			t.ResolveSpawnAgent(r.ID, r.Status)
		}
	case tools.ListAgentsToolName, tools.WaitAgentToolName:
		for _, r := range parseAgentRoster(output) {
			t.ResolveSpawnAgent(r.ID, r.Status)
		}
	}
}
