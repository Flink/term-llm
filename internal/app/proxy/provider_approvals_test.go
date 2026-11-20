package proxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type issuedBrowserCaller struct {
	Caller Caller `json:"caller"`
	Token  string `json:"token"`
}

func approvalRoute(name, provider string) Route {
	var profile string
	if provider == "service" {
		profile = "gemini"
		if name == "live" {
			profile = "gemini-live"
		}
	}
	return Route{Name: name, Profile: profile, LocalPrefix: "/" + name + "/", Upstream: "https://upstream.example/" + name, Enabled: true, Provider: provider}
}

func TestProviderApprovalDeltas(t *testing.T) {
	s := testService(t)
	routes := []Route{approvalRoute("api", "service"), approvalRoute("live", "service"), approvalRoute("other", ""), approvalRoute("extra", "")}
	routes[1].Enabled = false // Approvals include configured routes, not just enabled ones.
	configure(t, s, Config{Routes: routes}, nil)
	cookie, csrf := loginAdmin(t, s)
	request := func(method string, body map[string]any) *httptest.ResponseRecorder {
		return adminRequest(s, method, "callers", body, cookie, csrf)
	}
	issued := proxyResponse[issuedBrowserCaller](t, request("POST", map[string]any{"name": "provider", "providers": []string{"service"}, "revision": s.snap.revision}), 201)
	if !slices.Equal(issued.Caller.Grants, []string{"api", "live"}) {
		t.Fatalf("provider create grants = %v", issued.Caller.Grants)
	}
	legacy := proxyResponse[issuedBrowserCaller](t, request("POST", map[string]any{"name": "mixed", "grants": []string{"api", "other"}}), 201)
	machineKey := issueAdminKey(t, s, cookie, csrf, "Machine")
	machine := createMachineClient(t, s, machineKey.Token, "mixed machine client", []string{"api", "other"}, "")
	for _, c := range []issuedBrowserCaller{legacy, {Caller: machine.Client, Token: machine.Token}} {
		patch := func(changes ...map[string]any) {
			proxyResponse[map[string]bool](t, request("PUT", map[string]any{"id": c.Caller.ID, "provider_changes": changes, "revision": s.snap.revision}), 200)
		}
		patch(map[string]any{"id": "extra", "approved": true})
		assertApprovalGrants(t, s, c.Caller.ID, "api", "extra", "other")
		patch(map[string]any{"id": "service", "approved": true}, map[string]any{"id": "extra", "approved": false})
		assertApprovalGrants(t, s, c.Caller.ID, "api", "live", "other")
		if _, err := s.store.authorize(c.Token, s.snap.routes[0]); err != nil {
			t.Fatalf("approval changed the existing token: %v", err)
		}
		// Simulate an intervening authority change stored by another writer; do
		// not rebind that untouched grant or discard its existing DB record.
		if _, err := s.store.db.Exec("UPDATE grants SET authority='stored-authority' WHERE caller=? AND route='other'", c.Caller.ID); err != nil {
			t.Fatal(err)
		}
		patch(map[string]any{"id": "service", "approved": false})
		assertApprovalGrants(t, s, c.Caller.ID, "other")
		if _, err := s.store.authorize(c.Token, s.snap.routes[2]); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("revoke rebound an unrelated authority: %v", err)
		}
	}
}

func TestProviderApprovalRequiresCurrentConfiguration(t *testing.T) {
	s := testService(t)
	initial := approvalRoute("api", "service")
	configure(t, s, Config{Routes: []Route{initial}}, nil)
	cookie, csrf := loginAdmin(t, s)
	old := s.snap.revision
	issued := proxyResponse[issuedBrowserCaller](t, adminRequest(s, "POST", "callers", map[string]any{"name": "client", "providers": []string{"service"}, "revision": old}, cookie, csrf), 201)
	initial.Upstream += "/new"
	configure(t, s, Config{Routes: []Route{initial, approvalRoute("live", "service")}}, nil)
	assertApprovalGrants(t, s, issued.Caller.ID)
	patch := map[string]any{"id": issued.Caller.ID, "provider_changes": []map[string]any{{"id": "service", "approved": true}}}
	for _, tt := range []struct {
		rev  *int64
		code int
	}{{nil, 400}, {&old, 409}, {&s.snap.revision, 200}} {
		patch["revision"] = tt.rev
		proxyResponse[map[string]any](t, adminRequest(s, "PUT", "callers", patch, cookie, csrf), tt.code)
	}
	assertApprovalGrants(t, s, issued.Caller.ID, "api", "live")
	proxyResponse[map[string]any](t, adminRequest(s, "POST", "callers", map[string]any{"name": "stale", "providers": []string{"service"}, "revision": old}, cookie, csrf), 409)
}

func TestProviderApprovalRejectsMalformedChanges(t *testing.T) {
	s := testService(t)
	configure(t, s, Config{Routes: []Route{approvalRoute("api", "service")}}, nil)
	cookie, csrf := loginAdmin(t, s)
	c, _, err := s.store.createCaller("keep", []string{"api"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{
		{"provider_changes": nil},
		{"providers": []string{"service"}},
		{"provider_changes": []map[string]any{{"id": "service"}}},
		{"provider_changes": []map[string]any{{"id": "service", "approved": false}, {"id": "service", "approved": true}}},
		{"provider_changes": []map[string]any{{"id": "service", "approved": false}, {"id": "unknown", "approved": true}}},
		{"provider_changes": []map[string]any{}, "grants": nil},
	} {
		body["id"], body["revision"] = c.ID, s.snap.revision
		proxyResponse[map[string]any](t, adminRequest(s, "PUT", "callers", body, cookie, csrf), 400)
		assertApprovalGrants(t, s, c.ID, "api")
	}
	proxyResponse[map[string]any](t, adminRequest(s, "PUT", "callers", map[string]any{"id": c.ID, "revision": s.snap.revision, "provider_changes": []map[string]any{}}, cookie, csrf), 200)
	assertApprovalGrants(t, s, c.ID, "api")
}

func assertApprovalGrants(t *testing.T, s *Service, id string, want ...string) {
	t.Helper()
	got, err := selectStrings(context.Background(), s.store.db, "SELECT route FROM grants WHERE caller=? ORDER BY route", id)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("grants = %v, want %v: %v", got, want, err)
	}
}

func TestProviderApprovalSharedWorkerReadiness(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	launch := proxyResponse[nativeSetupReply](t, adminRequest(s, "POST", "claude/setup", map[string]any{"revision": s.snap.revision, "shared_action": "create"}, cookie, csrf), 201)
	route := s.snap.routes[0].Route
	providerBody := map[string]any{"name": "worker", "providers": []string{route.Name}, "revision": s.snap.revision}
	nativeBody := map[string]any{"route": route.Name, "revision": s.snap.revision}
	for endpoint, body := range map[string]any{"callers": providerBody, "claude/setup": nativeBody} {
		w := adminRequest(s, "POST", endpoint, body, cookie, csrf)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "shared Claude subscription") {
			t.Fatalf("unready shared worker: %s → %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	assertApprovalGrants(t, s, launch.CallerID, route.Name)
}

func TestNativeSetupApprovesEntireCompatibleProvider(t *testing.T) {
	for _, path := range []string{"claude/setup", "agy/setup"} {
		t.Run(path, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			nativeSetupPost(t, s, path, cookie, csrf, "create", "")
			anchor := s.snap.routes[0].Route
			sibling := anchor
			sibling.Name, sibling.LocalPrefix = "sibling", "/sibling/"
			configure(t, s, Config{Routes: []Route{anchor, sibling}}, nil)
			markNativeSetupAccountReady(t, s, anchor.Account, [32]byte{})
			issued := nativeSetupPost(t, s, path, cookie, csrf, "", anchor.Name)
			assertApprovalGrants(t, s, issued.CallerID, anchor.Name, sibling.Name)
			for _, route := range s.snap.routes {
				if _, err := s.store.authorize(issued.Token, route); err != nil {
					t.Fatalf("token was not approved for %q: %v", route.Name, err)
				}
			}
			incompatible := approvalRoute("non-native", anchor.Provider)
			configure(t, s, Config{Routes: []Route{anchor, sibling, incompatible}}, nil)
			rev := s.snap.revision
			w := adminRequest(s, "POST", path, map[string]any{"revision": rev, "route": anchor.Name}, cookie, csrf)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "incompatible native route") || s.snap.revision != rev {
				t.Fatalf("incompatible group did not fail without changing the configuration: %d %s", w.Code, w.Body.String())
			}
			if proxyCount(t, s.store, "SELECT count(*) FROM callers") != 2 {
				t.Fatal("incompatible group issued a partial native token")
			}
		})
	}
}

func TestNativeSetupOperatorGrantsOnlyEnrollmentRoute(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	proxyResponse[nativeSetupReply](t, adminRequest(s, "POST", "claude/setup", map[string]any{"revision": s.snap.revision, "shared_action": "create"}, cookie, csrf), 201)
	anchor := s.snap.routes[0].Route
	other := claudeSetupPost(t, s, cookie, csrf)
	second := s.snap.routes[1].Route
	second.Provider = anchor.Provider
	configure(t, s, Config{Routes: []Route{anchor, second}}, nil)
	operator := proxyResponse[nativeSetupReply](t, adminRequest(s, "POST", "claude/setup", map[string]any{"revision": s.snap.revision, "route": anchor.Name, "shared_action": "connect"}, cookie, csrf), 201)
	assertApprovalGrants(t, s, operator.CallerID, anchor.Name)
	assertApprovalGrants(t, s, other.CallerID, second.Name)
}

func TestProviderGroupingAccountKindCannotChangeAtSameRevision(t *testing.T) {
	s := testService(t)
	a := accountRecord{AccountInfo: AccountInfo{Name: "work", Kind: "chatgpt", State: "ready", Generation: 1}}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	main := providerTestRoute(t, "chatgpt", a.Name)
	sibling := providerTestRoute(t, "chatgpt-backend", a.Name)
	configure(t, s, Config{Routes: []Route{main, sibling}}, nil)
	cookie, csrf := loginAdmin(t, s)
	rev := s.snap.revision
	proxyResponse[map[string]any](t, adminRequest(s, "PUT", "accounts", map[string]any{"name": a.Name, "kind": "grok", "replace": true}, cookie, csrf), 409)
	proxyResponse[map[string]any](t, adminRequest(s, "DELETE", "accounts", map[string]any{"name": a.Name, "remove": true}, cookie, csrf), 409)
	a.Generation++
	a.Kind = "grok"
	if err := s.store.replaceAccount(context.Background(), a, 1); err == nil {
		t.Fatal("store permitted changing a referenced account's kind")
	}
	issued := proxyResponse[issuedBrowserCaller](t, adminRequest(s, "POST", "callers", map[string]any{"name": "same revision", "providers": []string{main.Name}, "revision": rev}, cookie, csrf), 201)
	assertApprovalGrants(t, s, issued.Caller.ID, main.Name, sibling.Name)
}
