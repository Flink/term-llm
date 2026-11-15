package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/hub"
	"github.com/term-llm/term-llm/internal/session"
)

func TestSessionsStatusActiveChildren(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(session.Config{Enabled: true, Path: filepath.Join(t.TempDir(), "sessions.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, sess := range []*session.Session{
		{ID: "parent"},
		{ID: "child", ParentID: "parent", IsSubagent: true},
		{ID: "grandchild", ParentID: "child", IsSubagent: true},
	} {
		sess.Provider, sess.Model, sess.Mode = "test", "test", session.ModeChat
		if err := store.Create(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	runs := newServeResponseRunManager()
	defer runs.Close()
	srv := &serveServer{store: store, responseRuns: runs}
	check := func(want bool) string {
		t.Helper()
		rr := httptest.NewRecorder()
		srv.handleSessionsStatus(rr, httptest.NewRequest("GET", "/v1/sessions/status?ids=parent", nil))
		if rr.Code != 200 {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		var body struct {
			Sessions []struct {
				ActiveRun        bool   `json:"active_run"`
				ActiveChildren   bool   `json:"active_children"`
				ActiveResponseID string `json:"active_response_id"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Sessions) != 1 {
			t.Fatalf("sessions = %s", rr.Body.String())
		}
		got := body.Sessions[0]
		if got.ActiveChildren != want || got.ActiveRun || got.ActiveResponseID != "" {
			t.Fatalf("parent status = %+v, want active children %v without parent response", got, want)
		}
		return rr.Header().Get("ETag")
	}
	idleETag := check(false)
	runs.setActiveRun("grandchild", "grandchild-response")
	activeETag := check(true)
	if activeETag == idleETag {
		t.Fatal("child activity did not change status ETag")
	}
	runs.clearActiveRun("grandchild", "grandchild-response")
	check(false)
	// A hosted child remains live between response turns, too.
	handle := srv.ensureChildRuns().ChildRunStarted(childRunInfo{ChildSessionID: "grandchild", ParentSessionID: "child"})
	check(true)
	handle.Finish(session.StatusComplete, nil)
	check(false)

	// Default polling must backfill an old parent without exposing its children.
	for i := range 201 {
		if err := store.Create(ctx, &session.Session{
			ID: fmt.Sprintf("recent-%d", i), Provider: "test", Model: "test",
			CreatedAt: time.Now().Add(time.Hour), UpdatedAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	runs.setActiveRun("grandchild", "grandchild-response-2")
	rr := httptest.NewRecorder()
	srv.handleSessionsStatus(rr, httptest.NewRequest("GET", "/v1/sessions/status", nil))
	var body struct {
		Sessions []struct {
			ID             string `json:"id"`
			ActiveChildren bool   `json:"active_children"`
		} `json:"sessions"`
	}
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &body) != nil {
		t.Fatalf("default status = %d: %s", rr.Code, rr.Body.String())
	}
	found := false
	for _, entry := range body.Sessions {
		if entry.ID == "child" || entry.ID == "grandchild" {
			t.Fatalf("child leaked into status: %+v", entry)
		}
		if entry.ID == "parent" {
			found = entry.ActiveChildren
		}
	}
	if !found || len(body.Sessions) != 201 {
		t.Fatalf("old active parent not backfilled: found=%v, rows=%d", found, len(body.Sessions))
	}
}

type childActivityCountingStore struct {
	session.Store
	gets int
}

func (s *childActivityCountingStore) Get(context.Context, string) (*session.Session, error) {
	s.gets++
	return nil, fmt.Errorf("unexpected session lookup")
}

func TestActiveChildParentsReusesTopLevelSummaries(t *testing.T) {
	store := &childActivityCountingStore{}
	srv := &serveServer{store: store}
	got := srv.activeChildParents(context.Background(), map[string]bool{"parent": true}, []session.SessionSummary{{ID: "parent"}})
	if store.gets != 0 || len(got) != 0 {
		t.Fatalf("parent projection = %v, session lookups = %d", got, store.gets)
	}
}

func TestHubNodeSessionsCountsActiveChildrenOnce(t *testing.T) {
	srv := &hubServer{}
	view := srv.buildHubNodeSessionsView(hub.Node{ID: "node"}, []hubNodeSessionStatus{
		{ID: "child-only", ActiveChildren: true},
		{ID: "both", ActiveRun: true, ActiveChildren: true},
		{ID: "idle"},
	})
	if view.ActiveCount != 2 || len(view.Active) != 2 {
		t.Fatalf("active sessions = %+v", view)
	}
	if !view.Active[0].ActiveChildren || view.Active[0].ActiveRun {
		t.Fatalf("child-only activity was conflated with a parent response: %+v", view.Active[0])
	}
}
