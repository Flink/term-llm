package filetrack

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountAllRunChangesMetadataOnly(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	if got, err := store.CountAllRunChanges(ctx, "session"); err != nil || got != 0 {
		t.Fatalf("empty count = %d, %v", got, err)
	}
	for _, rec := range []ChangeRecord{
		{SessionID: "session", RunID: "run-1", Path: "/work/modified", Before: []byte("a\n"), After: []byte("b\n")},
		{SessionID: "session", RunID: "run-2", Path: "/work/modified", Before: []byte("b\n"), After: []byte("c\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/created", BeforeMissing: true, After: []byte("new\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/deleted", Before: []byte("old\n"), AfterMissing: true},
		{SessionID: "session", RunID: "run-1", Path: "/work/unknown", BeforeUnknown: true, After: []byte("after\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/binary", BeforeMissing: true, After: []byte{0, 1, 0, 2}},
		{SessionID: "session", RunID: "run-1", Path: "/work/no-op", BeforeMissing: true, After: []byte("temporary\n")},
		{SessionID: "session", RunID: "run-2", Path: "/work/no-op", Before: []byte("temporary\n"), AfterMissing: true},
		{SessionID: "session", RunID: "run-1", Path: "/work/reverted", Before: []byte("base\n"), After: []byte("edited\n")},
		{SessionID: "session", RunID: "run-2", Path: "/work/reverted", Before: []byte("edited\n"), After: []byte("base\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/unverified", BeforeMissing: true, After: []byte("legacy\n")},
		{SessionID: "session", Path: "/work/no-run", BeforeMissing: true, After: []byte("legacy\n")},
		{SessionID: "session", RunID: "orphan-run", Path: "/work/orphan", BeforeMissing: true, After: []byte("orphan\n")},
		{SessionID: "other", RunID: "run-1", Path: "/work/other", BeforeMissing: true, After: []byte("other\n")},
	} {
		if _, err := recordTestChange(ctx, store, rec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE file_changes SET provenance='legacy_unverified' WHERE path='/work/unverified';
		DELETE FROM filetrack_runs WHERE run_id='orphan-run'`); err != nil {
		t.Fatal(err)
	}
	changes, err := store.ListRecentRunChanges(ctx, "session", -1)
	if err != nil || len(changes) != 5 {
		t.Fatalf("all-run changes = %#v, %v, want 5", changes, err)
	}
	// Even an absent blobs table cannot affect the startup count. It counts
	// attributed paths regardless of content retention, not renderable blobs.
	if _, err := store.db.Exec(`DROP TABLE blobs`); err != nil {
		t.Fatal(err)
	}
	if got, err := store.CountAllRunChanges(ctx, "session"); err != nil || got != len(changes) {
		t.Fatalf("metadata-only count = %d, %v, want %d", got, err, len(changes))
	}
	if got, err := store.CountAllRunChanges(ctx, "other"); err != nil || got != 1 {
		t.Fatalf("other session count = %d, %v", got, err)
	}
	if got, err := store.CountAllRunChanges(ctx, "absent"); err != nil || got != 0 {
		t.Fatalf("absent session count = %d, %v", got, err)
	}
}

func TestAllRunChangesAvoidRunIDPlaceholders(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	if _, err := recordTestChange(ctx, store, ChangeRecord{
		SessionID: "session", RunID: "run-1", Path: "/work/file", BeforeMissing: true, After: []byte("file\n"),
	}); err != nil {
		t.Fatal(err)
	}
	// More than SQLite's default 32766 variable limit. Large run histories
	// must not generate a placeholder per run for an all-turn refresh.
	if _, err := store.db.Exec(`WITH RECURSIVE runs(n) AS (
		VALUES(2) UNION ALL SELECT n+1 FROM runs WHERE n<33000
	) INSERT INTO filetrack_runs(session_id, run_id, ordinal, started_at)
		SELECT 'session', 'run-' || n, n, CURRENT_TIMESTAMP FROM runs`); err != nil {
		t.Fatal(err)
	}
	changes, err := store.ListRecentRunChanges(ctx, "session", -1)
	if err != nil || len(changes) != 1 || changes[0].Adds != 1 || changes[0].SnapshotSeq != 1 {
		t.Fatalf("all-run changes = %#v, %v", changes, err)
	}
}

// Compare cold counts to repeated all-turn refreshes over the same history.
// Both cases still query spans and retention; warm lists skip blob loads,
// decompression and line diffs, and keep only bounded scalar cache entries.
func BenchmarkListRecentRunChangesAllTurns(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "history.db"), Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	ctx := context.Background()
	const files, runs = 12, 8
	previous := make([][]byte, files)
	for run := 0; run < runs; run++ {
		for file := 0; file < files; file++ {
			before := previous[file]
			if before == nil {
				before = []byte(strings.Repeat(fmt.Sprintf("original file %d\n", file), 512))
			}
			after := []byte(strings.Repeat(fmt.Sprintf("file %d turn %d\n", file, run), 512))
			if _, err := recordTestChange(ctx, store, ChangeRecord{
				SessionID: "session", RunID: fmt.Sprintf("run-%d", run), Path: fmt.Sprintf("/work/file-%d.txt", file),
				Before: before, After: after,
			}); err != nil {
				b.Fatal(err)
			}
			previous[file] = after
		}
	}
	refresh := func(b *testing.B) {
		b.Helper()
		changes, err := store.ListRecentRunChanges(ctx, "session", -1)
		if err != nil || len(changes) != files || changes[0].Adds != 512 || changes[0].Dels != 512 {
			b.Fatalf("all-run refresh = %#v, %v", changes, err)
		}
	}
	b.Run("cold-counts", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			store.changeCounts = changeCountCache{}
			refresh(b)
		}
	})
	b.Run("warm-counts", func(b *testing.B) {
		refresh(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			refresh(b)
		}
	})
}
