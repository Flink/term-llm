package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Snapshot durable rows, including credential ciphertext, for rollback checks.
// Table names are fixed test-owned constants, never request input.
func claudeSetupDBState(t *testing.T, s *Service, tables ...string) string {
	t.Helper()
	state := map[string][]any{}
	for _, table := range tables {
		rows, err := s.store.db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, targets := make([]any, len(cols)), make([]any, len(cols))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			state[table] = append(state[table], values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNativeSetupSecurity(t *testing.T) {
	for _, endpoint := range []string{"claude/setup", "agy/setup"} {
		t.Run(endpoint, func(t *testing.T) { testNativeSetupSecurity(t, endpoint) })
	}
}

func testNativeSetupSecurity(t *testing.T, endpoint string) {
	for _, method := range []string{"POST", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			nativeSetupPost(t, s, endpoint, cookie, csrf, "create", "")
			before := claudeSetupDBState(t, s, "configuration", "accounts", "callers", "grants", "claude_shared_enrollment")
			snap := s.snap
			for _, tc := range []struct {
				name, origin, csrf string
				auth               bool
				body               string
				status             int
			}{
				{"unauthenticated", "http://localhost", csrf, false, fmt.Sprintf(`{"revision":%d}`, s.snap.revision), 401},
				{"no csrf", "http://localhost", "", true, fmt.Sprintf(`{"revision":%d}`, s.snap.revision), 403},
				{"wrong csrf", "http://localhost", "bad", true, fmt.Sprintf(`{"revision":%d}`, s.snap.revision), 403},
				{"no origin", "", csrf, true, fmt.Sprintf(`{"revision":%d}`, s.snap.revision), 403},
				{"foreign origin", "https://evil.test", csrf, true, fmt.Sprintf(`{"revision":%d}`, s.snap.revision), 403},
				{"missing revision", "http://localhost", csrf, true, `{}`, 400},
				{"null revision", "http://localhost", csrf, true, `{"revision":null}`, 400},
				{"stale revision", "http://localhost", csrf, true, fmt.Sprintf(`{"revision":%d}`, s.snap.revision-1), 409},
				{"other provider", "http://localhost", csrf, true, fmt.Sprintf(`{"revision":%d,"provider":"unexpected"}`, s.snap.revision), 400},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := httptest.NewRequest(method, "http://localhost/_proxy/api/"+endpoint, strings.NewReader(tc.body))
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Origin", tc.origin)
					r.Header.Set("X-CSRF-Token", tc.csrf)
					if tc.auth {
						r.AddCookie(cookie)
					}
					w := httptest.NewRecorder()
					s.ServeHTTP(w, r)
					if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("got %d %s", w.Code, w.Body.String())
					}
				})
			}
			s.opts.ConfigFile = "synthetic-operator-file-not-read"
			w := adminRequest(s, method, endpoint, map[string]any{"revision": s.snap.revision}, cookie, csrf)
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			if s.snap != snap || claudeSetupDBState(t, s, "configuration", "accounts", "callers", "grants", "claude_shared_enrollment") != before {
				t.Fatal("rejected operation wrote state")
			}
		})
	}
}

func TestClaudeSetupNeedsOnlyPasskeySession(t *testing.T) {
	s, _ := passkeyFixture(t)
	b := newPasskeyBrowser(t, s)
	_, _ = bootstrapPasskey(t, b)
	for _, method := range []string{"POST", "DELETE"} {
		body := map[string]any{"revision": s.snap.revision}
		want := 201
		if method == "DELETE" {
			want = 200
		}
		out := b.send(method, "claude/setup", body, want)
		if method == "POST" {
			var reply nativeSetupReply
			if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil || reply.URL != passkeyOrigin+s.snap.routes[0].LocalPrefix {
				t.Fatalf("untrusted response URL: %+v %v", reply, err)
			}
		}
	}
}

func TestClaudeSetupRollback(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			if method == "DELETE" {
				claudeSetupPost(t, s, cookie, csrf)
			}
			snap := s.snap
			beforeConfig := fmt.Sprintf("%+v", snap.config)
			before := claudeSetupDBState(t, s, "configuration", "money_settings", "accounts", "secrets", "callers", "grants", "claude_shared_enrollment")
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			s.sockets.Store("test", &activeSocket{route: "synthetic", account: "claude-bin", cancel: cancel})
			defer s.sockets.Delete("test")
			trigger := "CREATE TRIGGER setup_fault BEFORE INSERT ON grants BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END"
			if method == "DELETE" {
				trigger = "CREATE TRIGGER setup_fault BEFORE UPDATE ON configuration BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END"
			}
			if _, err := s.store.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			out := adminRequest(s, method, "claude/setup", map[string]any{"revision": snap.revision}, cookie, csrf)
			if out.Code != 503 || strings.Contains(out.Body.String(), "sk-tlp-") {
				t.Fatalf("failure returned token or wrong status: %d %s", out.Code, out.Body.String())
			}
			if s.snap != snap || fmt.Sprintf("%+v", snap.config) != beforeConfig || context.Cause(ctx) != nil {
				t.Fatal("failed commit published config or canceled a live socket")
			}
			if after := claudeSetupDBState(t, s, "configuration", "money_settings", "accounts", "secrets", "callers", "grants", "claude_shared_enrollment"); !reflect.DeepEqual(before, after) {
				t.Fatalf("transaction was not atomic\nbefore=%s\nafter=%s", before, after)
			}
			var synchronous int
			if err := s.store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 1 {
				t.Fatalf("custody wrapper did not restore NORMAL: %d %v", synchronous, err)
			}
		})
	}
}
