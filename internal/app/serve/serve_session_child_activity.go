package serve

import (
	"context"

	"github.com/term-llm/term-llm/internal/app/serve/sessionactivity"
	"github.com/term-llm/term-llm/internal/session"
)

// activeChildParents rolls live delegated execution up to its conversation.
// It is presentation state only: a child's response must never become the
// parent's active response or cause a browser to attach to the child's stream.
func (s *serveServer) activeChildParents(ctx context.Context, activeIDs map[string]bool, sessions []session.SessionSummary) map[string]bool {
	parents := make(map[string]string)
	for _, sess := range sessions {
		parents[sess.ID] = "" // The ordinary status list excludes all child rows.
	}
	liveParents := make(map[string]bool)
	registry := s.ensureChildRuns()
	registry.mu.RLock()
	handles := make([]*childRunHandle, 0, len(registry.byChild))
	for _, handle := range registry.byChild {
		handles = append(handles, handle)
	}
	registry.mu.RUnlock()
	for _, handle := range handles {
		parents[handle.childSessionID] = handle.parentSessionID
		if handle.state().Live {
			liveParents[handle.parentSessionID] = true
		}
	}
	return sessionactivity.ActiveParents(ctx, s.store, activeIDs, parents, liveParents)
}
