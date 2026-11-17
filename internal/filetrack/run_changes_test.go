package filetrack

import (
	"context"
	"testing"
)

func TestAllRunDiffsPreservePinnedVisibility(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	if err := store.RecordRunStart(ctx, RunRecord{SessionID: "session", RunID: "future"}); err != nil {
		t.Fatal(err)
	}
	pngBase := []byte("\x89PNG\r\n\x1a\nbaseline")
	pngOne := []byte("\x89PNG\r\n\x1a\none")
	pngTwo := []byte("\x89PNG\r\n\x1a\ntwo")
	for _, rec := range []ChangeRecord{
		{SessionID: "session", Path: "/work/file.md", Before: []byte("legacy-before\n"), After: []byte("baseline\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/file.md", Before: []byte("baseline\n"), After: []byte("one\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/image.png", Before: pngBase, After: pngOne},
		{SessionID: "session", RunID: "future", Path: "/work/file.md", Before: []byte("one\n"), After: []byte("two\n")},
		{SessionID: "session", RunID: "future", Path: "/work/image.png", Before: pngOne, After: pngTwo},
		{SessionID: "session", RunID: "future", Path: "/work/new.md", BeforeMissing: true, After: []byte("new\n")},
		{SessionID: "session", RunID: "run-1", Path: "/work/file.md", Before: []byte("two\n"), After: []byte("latest\n")},
	} {
		if _, err := recordTestChange(ctx, store, rec); err != nil {
			t.Fatal(err)
		}
	}
	// Snapshot run membership comes from historical rows, not today's run
	// registry. A pre-registered future run and later edits in the SAME run
	// must not leak into the snapshot; neither may rows without run IDs.
	if _, err := store.db.Exec(`DELETE FROM filetrack_runs WHERE run_id='run-1'`); err != nil {
		t.Fatal(err)
	}
	const snapshot = 3
	for _, runs := range []int{-1, -3, 0, 1, 3} {
		content, err := store.GetRecentRunFileDiffContent(ctx, "session", "/work/file.md", runs, snapshot)
		if err != nil || content == nil || !content.ContentAvailable || string(content.Before) != "baseline\n" || string(content.After) != "one\n" {
			t.Fatalf("runs=%d pinned text diff = %#v, %v", runs, content, err)
		}
		for _, side := range []string{"before", "after"} {
			wantText, wantImage := "baseline\n", pngBase
			if side == "after" {
				wantText, wantImage = "one\n", pngOne
			}
			text, err := store.GetRecentRunFileDiffTextSide(ctx, "session", "/work/file.md", side, runs, snapshot)
			if err != nil || text == nil || string(text.Data) != wantText {
				t.Fatalf("runs=%d pinned %s text = %#v, %v", runs, side, text, err)
			}
			image, err := store.GetRecentRunFileDiffSide(ctx, "session", "/work/image.png", side, runs, snapshot)
			if err != nil || image == nil || string(image.Data) != string(wantImage) {
				t.Fatalf("runs=%d pinned %s image = %#v, %v", runs, side, image, err)
			}
		}
		if content, err := store.GetRecentRunFileDiffContent(ctx, "session", "/work/new.md", runs, snapshot); err != nil || content != nil {
			t.Fatalf("runs=%d future file leaked into snapshot: %#v, %v", runs, content, err)
		}
	}
	// Unpinned all-turn queries still use registered runs, excluding both the
	// removed run-1 and the legacy row. Only future's one->two remains.
	content, err := store.GetRecentRunFileDiffContent(ctx, "session", "/work/file.md", -1, 0)
	if err != nil || content == nil || string(content.Before) != "one\n" || string(content.After) != "two\n" {
		t.Fatalf("current registered-run diff = %#v, %v", content, err)
	}
}

func TestAllRunObservationsRegisterRunsAndExcludeLegacy(t *testing.T) {
	store := openTestStore(t, Options{})
	ctx := context.Background()
	for _, obs := range []FilesystemObservation{
		{SessionID: "session", RunID: "run-1", Classification: ObservationUnclaimed, ModifiedCount: 1},
		{SessionID: "session", RunID: "run-2", Classification: ObservationMaterialized, CreatedCount: 2},
		{SessionID: "session", Classification: ObservationUnclaimed, CreatedCount: 99},
		{SessionID: "other", RunID: "other-run", Classification: ObservationUnclaimed, CreatedCount: 88},
	} {
		if _, err := store.RecordFilesystemObservation(ctx, obs); err != nil {
			t.Fatal(err)
		}
		if obs.RunID != "" {
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM filetrack_runs WHERE session_id=? AND run_id=?`, obs.SessionID, obs.RunID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("observation did not register run: count=%d err=%v", count, err)
			}
		}
	}
	for _, tc := range []struct{ runs, want int }{{-1, 2}, {-3, 2}, {0, 1}, {1, 1}, {3, 2}} {
		observations, err := store.ListRecentRunObservations(ctx, "session", tc.runs)
		if err != nil || len(observations) != tc.want {
			t.Fatalf("runs=%d observations = %#v, %v", tc.runs, observations, err)
		}
		for _, obs := range observations {
			if obs.SessionID != "session" || obs.RunID == "" || obs.CreatedCount > 2 {
				t.Fatalf("legacy or other-session observation leaked: %#v", obs)
			}
		}
	}
}
