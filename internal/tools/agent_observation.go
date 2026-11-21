package tools

import (
	"context"

	"github.com/term-llm/term-llm/internal/session"
	"github.com/term-llm/term-llm/internal/tools/agentrun"
)

// ObserveAgentStatus waits for a local child's completion without attaching to
// its events, collecting its result, or affecting parent lifecycle delivery.
// For a child owned by another process it returns the current status instead.
func ObserveAgentStatus(ctx context.Context, store session.AgentRunStore, parent, child string) (string, error) {
	record, entry, err := lookupAgentRun(ctx, store, agentrun.CurrentOwner(), child, parent)
	if err != nil {
		return "", err
	}
	if entry == nil || agentTerminal(record.Status) {
		return record.Status, nil
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-entry.done:
		// Read this exact execution, even if collected or continued since.
		entry.manager.mu.Lock()
		defer entry.manager.mu.Unlock()
		return entry.record.Status, nil
	}
}
