package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage/poll"
)

func TestNativeUsageAdminSettingsAndAuth(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "usage"}, {"POST", "usage/refresh"}, {"PUT", "usage/settings"},
	} {
		if rr := adminRequest(s, endpoint.method, endpoint.path, nil, nil, ""); rr.Code != 401 {
			t.Fatalf("unauthenticated %s %s: %d", endpoint.method, endpoint.path, rr.Code)
		}
		if endpoint.method != "GET" {
			if rr := adminRequest(s, endpoint.method, endpoint.path, nil, cookie, ""); rr.Code != 403 {
				t.Fatalf("missing CSRF for %s: %d", endpoint.path, rr.Code)
			}
		}
	}
	get := func() poll.Snapshot {
		t.Helper()
		rr := adminRequest(s, "GET", "usage", nil, cookie, csrf)
		var out poll.Snapshot
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &out) != nil || out.Providers == nil {
			t.Fatalf("usage response: %d %s", rr.Code, rr.Body.String())
		}
		return out
	}
	if out := get(); out.IntervalMinutes != 15 || out.Refreshing || len(out.Providers) != 0 {
		t.Fatalf("initial usage: %+v", out)
	}
	for _, input := range []any{9, 1441, 10.5, "15", nil} {
		rr := adminRequest(s, "PUT", "usage/settings", map[string]any{"interval_minutes": input}, cookie, csrf)
		if rr.Code != 400 {
			t.Fatalf("invalid interval %v: %d %s", input, rr.Code, rr.Body.String())
		}
	}
	for _, input := range []int{10, 1440} {
		rr := adminRequest(s, "PUT", "usage/settings", map[string]int{"interval_minutes": input}, cookie, csrf)
		if rr.Code != 200 || get().IntervalMinutes != input {
			t.Fatalf("valid interval %d not applied: %d %s", input, rr.Code, rr.Body.String())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.nativeUsage.Snapshot().IntervalMinutes; got != 1440 {
		t.Fatalf("interval after restart = %d", got)
	}
}

func TestNativeUsageAdminRefreshForStandaloneOpen(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	original := s.nativeUsage
	synctest.Test(t, func(t *testing.T) {
		cache, err := poll.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		s.nativeUsage = cache
		defer func() {
			cache.Close()
			s.nativeUsage = original
		}()
		release := make(chan struct{})
		s.nativeUsage.Reconcile([]poll.Source{{ID: "fixture", Key: "non-public-key", Fetch: func(ctx context.Context) (nativeusage.Report, error) {
			select {
			case <-ctx.Done():
				return nativeusage.Report{}, ctx.Err()
			case <-release:
			}
			return nativeusage.Report{Groups: []nativeusage.Group{{Name: "Plan", Windows: []nativeusage.Window{{Label: "Week", DurationMinutes: 10080, UsedPercent: 17}}}}}, nil
		}}})
		if rr := adminRequest(s, "POST", "usage/refresh", nil, cookie, csrf); rr.Code != 200 {
			t.Fatalf("manual refresh: %d %s", rr.Code, rr.Body.String())
		}
		synctest.Wait()
		if rr := adminRequest(s, "POST", "usage/refresh", nil, cookie, csrf); rr.Code != 200 {
			t.Fatalf("joining refresh: %d", rr.Code)
		}
		close(release)
		synctest.Wait()
		time.Sleep(1250 * time.Millisecond)
		if rr := adminRequest(s, "POST", "usage/refresh", nil, cookie, csrf); rr.Code != 429 || rr.Header().Get("Retry-After") != "59" || !strings.Contains(rr.Body.String(), "cooldown") {
			t.Fatalf("cooldown: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
		}
		rr := adminRequest(s, "GET", "usage", nil, cookie, csrf)
		var out poll.Snapshot
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &out) != nil || out.Refreshing || len(out.Providers) != 1 {
			t.Fatalf("unexpected report: %d %s", rr.Code, rr.Body.String())
		}
		p := out.Providers[0]
		if p.ID != "fixture" || p.UpdatedAt == nil || p.UpdatedAt.Location() != time.UTC || p.Report == nil || p.Report.Groups[0].Windows[0].UsedPercent != 17 || strings.Contains(rr.Body.String(), "non-public-key") {
			t.Fatalf("incorrect or unsafe report: %s", rr.Body.String())
		}
		time.Sleep(11 * time.Minute)
		rr = adminRequest(s, "PUT", "usage/settings", map[string]int{"interval_minutes": 10}, cookie, csrf)
		if rr.Code != http.StatusOK {
			t.Fatalf("shorten interval: %d %s", rr.Code, rr.Body.String())
		}
		synctest.Wait()
		rr = adminRequest(s, "GET", "usage", nil, cookie, csrf)
		var updated poll.Snapshot
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &updated) != nil || len(updated.Providers) != 1 || updated.Providers[0].UpdatedAt == nil || !updated.Providers[0].UpdatedAt.After(*p.UpdatedAt) {
			t.Fatalf("shortening an overdue interval did not poll: %d %s", rr.Code, rr.Body.String())
		}

	})
}

func TestNativeUsageReconcilesConfig(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	route := Route{Name: "plan", LocalPrefix: "/plan/", Upstream: "https://openrouter.ai/api/v1", Enabled: true,
		CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "},
		SetHeaders: map[string]HeaderRule{"Authorization": {Secret: "plan-secret", Prefix: "Bearer "}}}
	if rr := adminRequest(s, "PUT", "secrets", map[string]string{"name": "plan-secret", "value": "fixture-secret"}, cookie, csrf); rr.Code != 200 && rr.Code != 201 {
		t.Fatalf("secret fixture: %d %s", rr.Code, rr.Body.String())
	}
	state := func(hasUsage bool, count int) {
		t.Helper()
		var flags struct {
			HasUsage bool `json:"has_native_usage"`
		}
		rr := adminRequest(s, "GET", "state", nil, cookie, csrf)
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &flags) != nil || flags.HasUsage != hasUsage {
			t.Fatalf("unexpected admin state: %d %s", rr.Code, rr.Body.String())
		}
		got := s.nativeUsage.Snapshot()
		if len(got.Providers) != count || got.Refreshing {
			t.Fatalf("unexpected inventory from Open without a worker: %+v", got)
		}
	}
	state(false, 0)
	for _, routes := range [][]Route{{route}, {}} {
		rr := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": routes}, cookie, csrf)
		if rr.Code != http.StatusOK {
			t.Fatalf("provider update: %d %s", rr.Code, rr.Body.String())
		}
		state(len(routes) != 0, len(routes))
	}
	// API namespace retains normal Origin checks as well as session/CSRF checks.
	req := httptest.NewRequest("POST", "http://localhost/_proxy/api/usage/refresh", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("refresh without Origin: %d", rr.Code)
	}
}

func TestNativeUsageReconcilesAccountSignOutWithoutConfigEdit(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	in := accountInput{Name: "subscription", Kind: "chatgpt", Credentials: accountCredentials{
		AccessToken: "fixture-access", RefreshToken: "fixture-refresh", AccountID: "fixture-identity", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}}
	if rr := adminRequest(s, "POST", "accounts", in, cookie, csrf); rr.Code != 201 {
		t.Fatalf("account fixture: %d %s", rr.Code, rr.Body.String())
	}
	route := Route{Name: "subscription", LocalPrefix: "/subscription/", Upstream: "https://chatgpt.com/backend-api/codex", Enabled: true, Account: in.Name,
		CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}}
	rr := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{route}}, cookie, csrf)
	if rr.Code != 200 || !s.nativeUsage.HasSources() {
		t.Fatalf("account source fixture: %d %s", rr.Code, rr.Body.String())
	}
	revision := s.snap.revision
	rr = adminRequest(s, "DELETE", "accounts", map[string]string{"name": in.Name}, cookie, csrf)
	if rr.Code != 200 || s.nativeUsage.HasSources() || s.snap.revision != revision {
		t.Fatalf("sign-out did not reconcile the usage inventory: %d %s", rr.Code, rr.Body.String())
	}
	in.Replace = true
	rr = adminRequest(s, "PUT", "accounts", in, cookie, csrf)
	if rr.Code != 200 || !s.nativeUsage.HasSources() || s.snap.revision != revision {
		t.Fatalf("reconnect did not reconcile the usage inventory: %d %s", rr.Code, rr.Body.String())
	}
}
