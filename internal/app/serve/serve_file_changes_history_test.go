package serve

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/filetrack"
)

func TestSessionFileChangesAllTurnsLargeHistory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := filetrack.Open(dbPath, filetrack.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := &serveServer{fileTrackStoreFn: func() *filetrack.Store { return store }}
	record := func(rec filetrack.ChangeRecord) {
		t.Helper()
		rec.SessionID, rec.ToolName = "session", "write_file"
		rec.Provenance, rec.ClaimCoverage, rec.BaselineState = filetrack.ProvenanceDirect, filetrack.CoverageComplete, filetrack.BaselineNormal
		if _, err := store.RecordAttributedChange(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	pngBefore, pngAfter := "\x89PNG\r\n\x1a\nbefore", "\x89PNG\r\n\x1a\nafter"
	record(filetrack.ChangeRecord{RunID: "run-1", Path: "/work/file.md", Before: []byte("before\n"), After: []byte("after\n")})
	record(filetrack.ChangeRecord{RunID: "run-2", Path: "/work/image.png", Before: []byte(pngBefore), After: []byte(pngAfter)})

	// Seed more than SQLite's default 32766 parameter limit in both the run
	// registry and visible history, so even pinned detail windows exceed it.
	// Subsequent rows retain the same text endpoints; blobs need no copying.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`WITH RECURSIVE runs(n) AS (
		VALUES(3) UNION ALL SELECT n+1 FROM runs WHERE n<33000
	) INSERT INTO filetrack_runs(session_id,run_id,ordinal,started_at)
		SELECT 'session','run-' || n,n,CURRENT_TIMESTAMP FROM runs;
		INSERT INTO file_changes(session_id,run_id,seq,path,kind,before_hash,after_hash,provenance,baseline_state,content_status,event_seq)
		SELECT r.session_id,r.run_id,r.ordinal,c.path,c.kind,c.after_hash,c.after_hash,c.provenance,c.baseline_state,c.content_status,r.ordinal
		FROM filetrack_runs r JOIN file_changes c ON c.session_id=r.session_id AND c.seq=1
		WHERE r.session_id='session' AND r.ordinal>=3;
		UPDATE filetrack_event_counters SET next_event_seq=33001 WHERE session_id='session'`); err != nil {
		t.Fatal(err)
	}
	for _, obs := range []filetrack.FilesystemObservation{
		{SessionID: "session", RunID: "run-33000", Classification: filetrack.ObservationMaterialized, CreatedCount: 2},
		{SessionID: "session", Classification: filetrack.ObservationUnclaimed, CreatedCount: 99},
		{SessionID: "other", RunID: "run-1", Classification: filetrack.ObservationUnclaimed, CreatedCount: 88},
	} {
		if _, err := store.RecordFilesystemObservation(ctx, obs); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/sessions/session/file-changes"
	code, body := getSessionPath(t, srv, base+"?scope=all_turns")
	if code != http.StatusOK || len(body["file_changes"].([]any)) != 2 || len(body["materializations"].([]any)) != 1 {
		t.Fatalf("large-history list status=%d body=%#v", code, body)
	}
	observations := body["observations"].(map[string]any)
	if observations["total_created"] != float64(2) || len(observations["batches"].([]any)) != 0 {
		t.Fatalf("all-turn observations leaked other sessions or legacy rows: %#v", observations)
	}
	for _, raw := range body["file_changes"].([]any) {
		if change := raw.(map[string]any); change["snapshot_seq"] != float64(33000) {
			t.Fatalf("all-turn snapshot = %#v", change)
		}
	}

	record(filetrack.ChangeRecord{RunID: "run-33001", Path: "/work/file.md", Before: []byte("after\n"), After: []byte("latest\n")})
	pngLatest := "\x89PNG\r\n\x1a\nlatest"
	record(filetrack.ChangeRecord{RunID: "run-33001", Path: "/work/image.png", Before: []byte(pngAfter), After: []byte(pngLatest)})
	for _, snapshot := range []string{"", "&snapshot_seq=33000"} {
		wantText, wantImage := "latest\n", pngLatest
		if snapshot != "" {
			wantText, wantImage = "after\n", pngAfter
		}
		for _, path := range []string{"/work/file.md", "/work/image.png"} {
			code, diff := getSessionPath(t, srv, base+"/diff?scope=all_turns&path="+path+snapshot)
			if code != http.StatusOK || diff["content_available"] != true || diff["truncated"] != false {
				t.Fatalf("%s diff snapshot=%q status=%d body=%#v", path, snapshot, code, diff)
			}
			if strings.HasSuffix(path, ".md") {
				if !strings.Contains(fmt.Sprint(diff["hunks"]), strings.TrimSpace(wantText)) {
					t.Fatalf("snapshot=%q diff did not contain %q: %#v", snapshot, wantText, diff)
				}
			} else if diff["image"] != true {
				t.Fatalf("image diff = %#v", diff)
			}
		}
		for _, tc := range []struct{ path, side, want string }{
			{"/work/file.md", "before", "before\n"},
			{"/work/file.md", "after", wantText},
			{"/work/image.png", "before", pngBefore},
			{"/work/image.png", "after", wantImage},
		} {
			req := httptest.NewRequest(http.MethodGet, base+"/content?scope=all_turns&path="+tc.path+"&side="+tc.side+snapshot, nil)
			rr := httptest.NewRecorder()
			srv.handleSessionByID(rr, req)
			if rr.Code != http.StatusOK || rr.Body.String() != tc.want {
				t.Fatalf("%s %s snapshot=%q status=%d body=%q", tc.path, tc.side, snapshot, rr.Code, rr.Body.String())
			}
		}
	}
}
