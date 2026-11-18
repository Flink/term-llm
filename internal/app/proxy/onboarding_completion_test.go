package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestOnboardingCompletionSurvivesRetentionAndRestart(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	check := func(s *Service, cookie *http.Cookie, csrf string, want bool) {
		t.Helper()
		got, err := s.store.hasRequests(context.Background())
		if err != nil || got != want {
			t.Fatalf("hasRequests = %v, %v; want %v", got, err, want)
		}
		out := adminRequest(s, "GET", "state", nil, cookie, csrf)
		var state struct {
			HasRequests *bool `json:"has_requests"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if out.Code != http.StatusOK || state.HasRequests == nil || *state.HasRequests != want {
			t.Fatalf("state has_requests: %d %s; want %v", out.Code, out.Body.String(), want)
		}
	}
	check(s, cookie, csrf, false)

	// A rejected admission must not count as persisted forwarding evidence.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rejected := Record{ID: "rejected", Outcome: "unknown"}
	if err := s.store.start(ctx, &rejected); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
	check(s, cookie, csrf, false)

	// Admission is enough: no terminal result or vendor request is required.
	rec := Record{ID: "accepted", Started: 1, CallerID: "caller", Route: "route", Method: "POST", Path: "/responses", Outcome: "unknown"}
	if err := s.store.start(context.Background(), &rec); err != nil {
		t.Fatal(err)
	}
	check(s, cookie, csrf, true)
	if err := s.store.retain(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.store.db.QueryRow("SELECT count(*) FROM requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention left %d request rows: %v", count, err)
	}
	check(s, cookie, csrf, true)

	opts := s.opts
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	cookie, csrf = loginAdmin(t, reopened)
	check(reopened, cookie, csrf, true)
}

func TestOnboardingCompletionQueryIsReadOnlyAndRequiresPositiveSequence(t *testing.T) {
	s := testService(t)
	for _, tt := range []struct {
		name string
		seq  int
		want bool
	}{
		{name: "zero", seq: 0, want: false},
		{name: "positive", seq: 1, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.store.db.Exec("DELETE FROM sqlite_sequence; INSERT INTO sqlite_sequence(name,seq) VALUES('unrelated',10),('requests',?)", tt.seq); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec("PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := s.store.db.Exec("PRAGMA query_only=OFF"); err != nil {
					t.Error(err)
				}
			})
			got, err := s.store.hasRequests(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("read-only completion query = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestOnboardingCompletionQueryHonorsContext(t *testing.T) {
	s := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.store.hasRequests(ctx); got || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled completion query = %v, %v", got, err)
	}
}

func TestOnboardingCompletionStateMetadataIsPrivate(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	const secret = "private-upstream-credential"
	out := adminRequest(s, "PUT", "secrets", map[string]string{"name": "key", "origin": "https://example.test", "value": secret}, cookie, csrf)
	if out.Code != http.StatusOK {
		t.Fatal(out.Code, out.Body.String())
	}
	a := accountRecord{
		AccountInfo: AccountInfo{Name: "account", Kind: "chatgpt", State: "ready", Generation: 1},
		credentials: accountCredentials{AccessToken: "private-access-token", RefreshToken: "private-refresh-token", AccountID: "private-account-id", GitHubToken: "private-github-token"},
	}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.store.createCaller("metadata caller", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{ID: "metadata-request", Outcome: "unknown"}
	if err := s.store.start(context.Background(), &rec); err != nil {
		t.Fatal(err)
	}
	out = adminRequest(s, "GET", "state", nil, cookie, csrf)
	var state map[string]json.RawMessage
	if err := json.Unmarshal(out.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusOK || string(state["has_requests"]) != "true" {
		t.Fatal(out.Code, out.Body.String())
	}
	for _, key := range []string{"accounts", "money", "mode", "auth", "revision", "routes", "secrets", "callers", "csrf", "retention_days", "presets"} {
		if _, ok := state[key]; !ok {
			t.Errorf("state omitted existing metadata %q", key)
		}
	}
	if !strings.Contains(string(state["secrets"]), `"name":"key"`) || !strings.Contains(string(state["accounts"]), `"name":"account"`) || !strings.Contains(string(state["callers"]), `"name":"metadata caller"`) {
		t.Fatal("missing seeded public metadata", out.Body.String())
	}
	for _, private := range []string{secret, a.credentials.AccessToken, a.credentials.RefreshToken, a.credentials.AccountID, a.credentials.GitHubToken, token, hashToken(token), cookie.Value, strings.Repeat("a", 64)} {
		if strings.Contains(out.Body.String(), private) {
			t.Error("state exposed private credentials")
		}
	}
}

func TestOnboardingCompletionStateReadFailureAndAuthentication(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	// Closing storage makes the completion query fail, without embedding the
	// database error in the response or allowing unauthenticated storage reads.
	if err := s.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	out := adminRequest(s, "GET", "state", nil, cookie, csrf)
	if out.Code != http.StatusServiceUnavailable || out.Body.String() != "{\"error\":\"Could not read request completion state\"}\n" {
		t.Fatalf("read failure: %d %s", out.Code, out.Body.String())
	}
	out = adminRequest(s, "GET", "state", nil, nil, "")
	if out.Code != http.StatusUnauthorized || out.Body.String() != "{\"error\":\"Sign in to administer this proxy\"}\n" {
		t.Fatalf("unauthorized state changed: %d %s", out.Code, out.Body.String())
	}
}
