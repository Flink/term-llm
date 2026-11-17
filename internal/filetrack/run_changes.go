package filetrack

import (
	"context"
	"database/sql"
	"fmt"
)

// allRunFilter keeps unpinned run membership in SQLite. Pinned windows instead
// resolve run visibility from file_changes at the selected sequence, exactly as
// latestRunWindow does: a nonempty run ID on a row at seq<=snapshot is itself
// proof that the run was visible. No current run-registry membership is needed.
func (s *Store) allRunFilter(ctx context.Context, sessionID string, snapshotSeq int64) (string, []any, int64, error) {
	if snapshotSeq > 0 {
		return ` AND COALESCE(run_id,'')<>'' AND seq<=?`, []any{snapshotSeq}, snapshotSeq, nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM file_changes WHERE session_id=?`, sessionID).Scan(&snapshotSeq); err != nil {
		return "", nil, 0, fmt.Errorf("query file change snapshot: %w", err)
	}
	return ` AND run_id IN (SELECT run_id FROM filetrack_runs WHERE session_id=?) AND seq<=?`,
		[]any{sessionID, snapshotSeq}, snapshotSeq, nil
}

func (s *Store) recentRunPathSpan(ctx context.Context, sessionID, path string, runs int, snapshotSeq int64) (*pathSpan, error) {
	if runs < 0 {
		filter, args, _, err := s.allRunFilter(ctx, sessionID, snapshotSeq)
		if err != nil {
			return nil, err
		}
		return s.sessionFilteredPathSpan(ctx, sessionID, path, filter, args)
	}
	window, err := s.latestRunWindow(ctx, sessionID, runs, snapshotSeq)
	if err != nil || len(window.runIDs) == 0 {
		return nil, err
	}
	return s.sessionRunPathSpan(ctx, sessionID, window.runIDs, window.snapshotSeq, path)
}

func (s *Store) sessionPathSpan(ctx context.Context, sessionID, path string) (*pathSpan, error) {
	return s.sessionRunPathSpan(ctx, sessionID, nil, 0, path)
}

func (s *Store) sessionRunPathSpan(ctx context.Context, sessionID string, runIDs []string, snapshotSeq int64, path string) (*pathSpan, error) {
	filter, args := runWindowFilter(runIDs, snapshotSeq)
	return s.sessionFilteredPathSpan(ctx, sessionID, path, filter, args)
}

func (s *Store) sessionFilteredPathSpan(ctx context.Context, sessionID, path, filter string, filterArgs []any) (*pathSpan, error) {
	path = normalizePath(path)
	if path == "" {
		return nil, nil
	}
	sp := &pathSpan{path: path}
	where := "session_id = ? AND path = ? AND provenance IN ('direct','declared_transform','declared_generate')"
	args := []any{sessionID, path}
	where += filter
	args = append(args, filterArgs...)
	if err := s.db.QueryRowContext(ctx, `
		SELECT kind, COALESCE(before_hash, ''), is_binary, baseline_state, content_status,
		       provenance, claim_coverage
		FROM file_changes
		WHERE `+where+`
		ORDER BY seq ASC LIMIT 1`, args...).
		Scan(&sp.firstKind, &sp.firstBeforeHash, &sp.firstBinary, &sp.firstBaselineState,
			&sp.firstContentStatus, &sp.provenance, &sp.claimCoverage); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query first path change: %w", err)
	}
	sp.provenances = []string{sp.provenance}
	rows, err := s.db.QueryContext(ctx, `SELECT provenance, claim_coverage FROM file_changes WHERE `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("query path provenance: %w", err)
	}
	for rows.Next() {
		var provenance, coverage string
		if err := rows.Scan(&provenance, &coverage); err != nil {
			rows.Close()
			return nil, err
		}
		if !containsString(sp.provenances, provenance) {
			sp.provenances = append(sp.provenances, provenance)
			sp.provenance = "mixed"
		}
		sp.claimCoverage = worstCoverage(sp.claimCoverage, coverage)
	}
	rows.Close()
	if err := s.db.QueryRowContext(ctx, `
		SELECT seq, kind, COALESCE(after_hash, ''), is_binary, content_status
		FROM file_changes
		WHERE `+where+`
		ORDER BY seq DESC LIMIT 1`, args...).
		Scan(&sp.lastSeq, &sp.lastKind, &sp.lastAfterHash, &sp.lastBinary, &sp.lastContentStatus); err != nil {
		return nil, fmt.Errorf("query last path change: %w", err)
	}
	return sp, nil
}

// GetRecentRunFileDiffContent returns one file diff across the latest runs that
// recorded file changes. A positive snapshotSeq pins the rolling window.
func (s *Store) GetRecentRunFileDiffContent(ctx context.Context, sessionID, path string, runs int, snapshotSeq int64) (*FileDiffContent, error) {
	sp, err := s.recentRunPathSpan(ctx, sessionID, path, runs, snapshotSeq)
	return s.fileDiffContentFromSpan(ctx, sp, err)
}

// GetRecentRunFileDiffSide returns one retained image side across the latest
// runs that recorded file changes. A positive snapshotSeq pins the window.
func (s *Store) GetRecentRunFileDiffSide(ctx context.Context, sessionID, path, side string, runs int, snapshotSeq int64) (*FileDiffSide, error) {
	if side != "before" && side != "after" {
		return nil, ErrInvalidDiffSide
	}
	sp, err := s.recentRunPathSpan(ctx, sessionID, path, runs, snapshotSeq)
	return s.fileDiffSideFromSpan(ctx, sp, side, err)
}

// GetRecentRunFileDiffTextSide returns one retained UTF-8 text side across the
// latest runs that recorded file changes. A positive snapshotSeq pins the window.
func (s *Store) GetRecentRunFileDiffTextSide(ctx context.Context, sessionID, path, side string, runs int, snapshotSeq int64) (*FileDiffTextSide, error) {
	if side != "before" && side != "after" {
		return nil, ErrInvalidDiffSide
	}
	sp, err := s.recentRunPathSpan(ctx, sessionID, path, runs, snapshotSeq)
	return s.fileDiffTextSideFromSpan(ctx, sp, side, err)
}
