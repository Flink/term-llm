package filetrack

import (
	"context"
	"fmt"
	"sort"
)

// ListRecentRunChanges returns the cumulative changes across the latest file
// tracking runs (negative means all), including an in-progress run. Legacy rows without run IDs are excluded.
func (s *Store) ListRecentRunChanges(ctx context.Context, sessionID string, runs int) ([]CumulativeChange, error) {
	if runs < 0 {
		return s.listAllRunChanges(ctx, sessionID)
	}
	window, err := s.latestRunWindow(ctx, sessionID, runs, 0)
	if err != nil || len(window.runIDs) == 0 {
		return nil, err
	}
	spans, err := s.sessionRunSpans(ctx, sessionID, window.runIDs, window.snapshotSeq)
	changes, err := s.listChangesFromSpans(ctx, spans, err)
	if err != nil {
		return nil, err
	}
	if runs != 1 {
		for i := range changes {
			changes[i].SnapshotSeq = window.snapshotSeq
		}
	}
	return changes, nil
}

// listAllRunChanges keeps run membership in SQL instead of materializing the
// entire run history and generating an unbounded number of IN placeholders.
func (s *Store) listAllRunChanges(ctx context.Context, sessionID string) ([]CumulativeChange, error) {
	filter, args, snapshotSeq, err := s.allRunFilter(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}
	spans, err := s.sessionFilteredSpans(ctx, sessionID, filter, args)
	changes, err := s.listChangesFromSpans(ctx, spans, err)
	for i := range changes {
		changes[i].SnapshotSeq = snapshotSeq
	}
	return changes, err
}

// CountAllRunChanges returns the number of non-no-op attributed paths across all
// known runs, excluding legacy rows without run IDs. It reads only row metadata:
// no blob bodies, decompression, or line diffs. This is an availability projection
// for startup, not a historical line-count summary or content-retention check.
func (s *Store) CountAllRunChanges(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		WITH spans AS (
			SELECT MIN(seq) AS first_seq, MAX(seq) AS last_seq
			FROM file_changes
			WHERE session_id=? AND path<>''
			  AND provenance IN ('direct','declared_transform','declared_generate')
			  AND run_id IN (SELECT run_id FROM filetrack_runs WHERE session_id=?)
			GROUP BY path
		)
		SELECT COUNT(*) FROM spans
		JOIN file_changes AS first ON first.session_id=? AND first.seq=spans.first_seq
		JOIN file_changes AS last ON last.session_id=first.session_id AND last.seq=spans.last_seq
		WHERE NOT (first.kind='create' AND last.kind='delete')
		  AND NOT (first.kind<>'create' AND last.kind<>'delete'
		           AND COALESCE(first.before_hash,'')<>'' AND first.before_hash=COALESCE(last.after_hash,''))`,
		sessionID, sessionID, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count all-run file changes: %w", err)
	}
	return count, nil
}

func (s *Store) listChangesFromSpans(ctx context.Context, spans []*pathSpan, err error) ([]CumulativeChange, error) {
	if err != nil {
		return nil, err
	}

	changes := make([]CumulativeChange, 0, len(spans))
	for _, sp := range spans {
		kind, ok := sp.resolve()
		if !ok {
			continue
		}

		change := CumulativeChange{
			Path: sp.path, Kind: kind, Seq: sp.lastSeq, Provenance: sp.provenance,
			Provenances: append([]string(nil), sp.provenances...), BaselineState: sp.firstBaselineState,
			ClaimCoverage: sp.claimCoverage, ContentStatus: cumulativeContentStatus(sp, kind),
		}

		needBefore, needAfter := blobsNeeded(kind)

		// Retained binaries are browser-renderable images. Their line counts are
		// always zero, so verify blob presence without reading and decompressing
		// potentially multi-megabyte image bodies on every list refresh.
		if sp.retainedImage(kind) {
			change.ContentAvailable = true
			if needBefore && !s.blobExists(ctx, sp.firstBeforeHash) {
				change.Truncated = true
				change.ContentAvailable = false
			}
			if needAfter && !s.blobExists(ctx, sp.lastAfterHash) {
				change.Truncated = true
				change.ContentAvailable = false
			}
			changes = append(changes, change)
			continue
		}
		// A binary side paired with a non-binary side is not a renderable image
		// comparison and must never be passed through line-based diffing.
		if sp.hasBinarySide(kind) {
			change.Truncated = true
			changes = append(changes, change)
			continue
		}

		change.Adds, change.Dels, change.ContentAvailable = s.cachedSpanCounts(ctx, sp, kind)
		change.Truncated = !change.ContentAvailable
		changes = append(changes, change)
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}
