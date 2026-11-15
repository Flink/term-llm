package sessionactivity

import (
	"context"

	"github.com/term-llm/term-llm/internal/session"
)

// ActiveParents rolls delegated activity up through durable session ancestry.
// parents contains known registry edges; liveParents seeds work between turns.
func ActiveParents(ctx context.Context, store session.Store, activeIDs map[string]bool, parents map[string]string, liveParents map[string]bool) map[string]bool {
	if parents == nil {
		parents = make(map[string]string)
	}
	if liveParents == nil {
		liveParents = make(map[string]bool)
	}
	parentOf := func(id string) string {
		if parent, ok := parents[id]; ok {
			return parent
		}
		parent := ""
		if store != nil {
			if sess, err := store.Get(ctx, id); err == nil && sess != nil {
				parent = sess.ParentID
			}
		}
		parents[id] = parent
		return parent
	}
	for id := range activeIDs {
		if parent := parentOf(id); parent != "" {
			liveParents[parent] = true
		}
	}
	result := make(map[string]bool)
	for id := range liveParents {
		// The shared result also bounds traversal and protects against malformed
		// cyclic ancestry without querying the same ancestor repeatedly.
		for id != "" && !result[id] {
			result[id] = true
			id = parentOf(id)
		}
	}
	return result
}
