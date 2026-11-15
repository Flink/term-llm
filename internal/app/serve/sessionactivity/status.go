package sessionactivity

import (
	"context"
	"errors"
	"log"

	"github.com/term-llm/term-llm/internal/session"
)

// ListStatusAttention reads a complete durable activity projection, retrying snapshot conflicts.
func ListStatusAttention(ctx context.Context, store session.AttentionStore, kind session.AttentionKind) map[string]session.AttentionItem {
	for attempt := 0; attempt < 2; attempt++ {
		candidate := make(map[string]session.AttentionItem)
		cursor := ""
		var snapshotVersion int64
		complete := true
		for {
			page, err := store.ListAttention(ctx, session.AttentionListOptions{
				Kind: kind, Limit: 500, Cursor: cursor, SnapshotVersion: snapshotVersion,
			})
			if err != nil {
				complete = false
				if !errors.Is(err, session.ErrAttentionConflict) || attempt == 1 {
					log.Printf("[serve] durable %s status projection unavailable: %v", kind, err)
				}
				break
			}
			if snapshotVersion == 0 {
				snapshotVersion = page.SnapshotVersion
			}
			for _, item := range page.Items {
				candidate[item.SessionID] = item
			}
			if !page.HasMore || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if complete {
			return candidate
		}
	}
	return map[string]session.AttentionItem{}
}

// RunningSessionDetail merges live response IDs with durable running activity.
func RunningSessionDetail(ctx context.Context, store session.Store, activeIDs map[string]bool) (map[string]bool, map[string]session.AttentionItem) {
	if activeIDs == nil {
		activeIDs = make(map[string]bool)
	}
	if store == nil {
		return activeIDs, nil
	}
	attentionStore, ok := session.AsAttentionStore(store)
	if !ok {
		return activeIDs, nil
	}
	durableRunning := ListStatusAttention(ctx, attentionStore, session.AttentionKindRunning)
	for id := range durableRunning {
		activeIDs[id] = true
	}
	return activeIDs, durableRunning
}
