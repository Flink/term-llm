package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Native login in the client-side token vault replaced browser pairing and the
// browser Claude OAuth flow. No legacy endpoint may still answer, and nothing
// may bypass ordinary admin cookie/CSRF handling.
func TestLegacyPairingAndLoginEndpointsAreGone(t *testing.T) {
	s := testService(t)
	for _, action := range []string{"start", "status", "commit", "ack", "cancel"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, "http://localhost/_proxy/api/connect/"+action, strings.NewReader(`{"id":"00000000000000000000000000000000"}`))
			req.Host = "localhost"
			req.Header.Set("Origin", "http://localhost")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.ServeHTTP(rr, req)
			// Without a pairing capability the request is anonymous administration,
			// so the only admissible answer is the ordinary sign-in rejection.
			if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "Sign in to administer this proxy") {
				t.Fatalf("connect/%s %s: %d %s", action, method, rr.Code, rr.Body.String())
			}
		}
	}
	cookie, csrf := loginAdmin(t, s)
	for _, path := range []string{"connect/start", "connect/status", "connect/commit", "connect/ack", "connect/cancel", "connect/request", "connect/approve", "accounts/claude/login/start", "accounts/claude/login/finish", "accounts/claude/login/cancel"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rr := adminRequest(s, method, path, map[string]string{"id": "legacy", "code": "legacy"}, cookie, csrf)
			if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "Unknown administration endpoint") {
				t.Fatalf("%s %s: %d %s", method, path, rr.Code, rr.Body.String())
			}
		}
	}
	for table, want := range map[string]string{"accounts": "SELECT count(*) FROM accounts", "callers": "SELECT count(*) FROM callers", "grants": "SELECT count(*) FROM grants"} {
		var n int
		if err := s.store.db.QueryRow(want).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s=%d err=%v", table, n, err)
		}
	}
	if len(s.snap.routes) != 0 {
		t.Fatalf("legacy pairing created %d routes", len(s.snap.routes))
	}
	for _, table := range []string{"connect_requests", "connect_managed"} {
		var n int
		if err := s.store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("superseded %s schema still present: %d %v", table, n, err)
		}
	}
}
