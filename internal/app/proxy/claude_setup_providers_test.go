package proxy

import (
	"context"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestNativeSetupProviderSeparation(t *testing.T) {
	s := testService(t)
	// A reserved-looking name is not evidence of the kind of any account.
	claudeSetupAccount(t, s, "agy-bin", "chatgpt")
	claudeSetupAccount(t, s, "claude-bin", "chatgpt")
	cookie, csrf := loginAdmin(t, s)
	claude := claudeSetupPost(t, s, cookie, csrf)
	agy := nativeSetupPost(t, s, "agy/setup", cookie, csrf, "create", "")
	if len(s.snap.routes) != 2 {
		t.Fatalf("setup created %d routes", len(s.snap.routes))
	}
	claudeRoute, agyRoute := s.snap.routes[0], s.snap.routes[1]
	if claudeRoute.Account != "claude-bin-2" || agyRoute.Account != "agy-bin-2" || agyRoute.Name != "agy-bin-2" ||
		agyRoute.Upstream != "https://daily-cloudcode-pa.googleapis.com" || agyRoute.Billing != "plan" ||
		agyRoute.CallerAuth != (Auth{Header: "Authorization", Prefix: "Bearer "}) || !strings.HasSuffix(agy.URL, "/agy-bin-2/") {
		t.Fatalf("unexpected route binding: %+v %+v", claudeRoute.Route, agyRoute.Route)
	}
	nativeSetupAssertMetadata(t, s, agy, agyRoute.Name, agyProxyProvider)
	req := httptest.NewRequest("GET", agy.URL+claudeProxyProvider.discovery, nil)
	req.URL.Scheme, req.URL.Host = "", ""
	req.Header.Set("Authorization", "Bearer "+agy.Token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("Antigravity grant yielded Claude discovery metadata: %d", w.Code)
	}
	for _, tc := range []struct {
		path   string
		issued nativeSetupReply
		route  activeRoute
		other  activeRoute
	}{
		{"claude/setup", claude, claudeRoute, agyRoute},
		{"agy/setup", agy, agyRoute, claudeRoute},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if c, err := s.store.authorize(tc.issued.Token, tc.route); err != nil || c.ID != tc.issued.CallerID {
				t.Fatalf("missing scoped grant: %v", err)
			}
			if _, err := s.store.authorize(tc.issued.Token, tc.other); err == nil {
				t.Fatal("client token can authorize the other provider's route")
			}
			before := claudeSetupDBState(t, s, "configuration", "accounts", "callers", "grants", "claude_shared_enrollment")
			w := adminRequest(s, "POST", tc.path, map[string]any{"revision": s.snap.revision, "route": tc.other.Name}, cookie, csrf)
			if w.Code != 400 || claudeSetupDBState(t, s, "configuration", "accounts", "callers", "grants", "claude_shared_enrollment") != before {
				t.Fatalf("adopted or modified the other provider: %d %s", w.Code, w.Body.String())
			}
			second := nativeSetupPost(t, s, tc.path, cookie, csrf, "connect", tc.route.Name)
			if second.URL != tc.issued.URL || second.Token == tc.issued.Token || len(s.snap.routes) != 2 {
				t.Fatal("reconnecting an operator must issue a new token without replacing or duplicating the route")
			}
		})
	}
}

func TestNativeSetupClearIsProviderScoped(t *testing.T) {
	for _, tc := range []struct {
		path  string
		index int
	}{
		{"claude/setup", 0}, {"agy/setup", 1},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			devices := [2]nativeSetupReply{claudeSetupPost(t, s, cookie, csrf), nativeSetupPost(t, s, "agy/setup", cookie, csrf, "create", "")}
			routes := [2]activeRoute{s.snap.routes[0], s.snap.routes[1]}
			providers := [2]*nativeProxyProvider{claudeProxyProvider, agyProxyProvider}
			mixed, token, err := s.store.createCaller("both providers", []string{routes[0].Name, routes[1].Name}, s.snap)
			if err != nil {
				t.Fatal(err)
			}
			for i, ar := range routes {
				markNativeSetupAccountReady(t, s, ar.Account, [32]byte{byte(i + 1)})
			}

			removed, retained := tc.index, 1-tc.index
			w := adminRequest(s, "DELETE", tc.path, map[string]any{"revision": s.snap.revision}, cookie, csrf)
			if w.Code != 200 || len(s.snap.routes) != 1 || !reflect.DeepEqual(s.snap.routes[0], routes[retained]) {
				t.Fatalf("clear did not preserve other route: %d %s", w.Code, w.Body.String())
			}
			infos, err := s.store.accountInfos()
			if err != nil || len(infos) != 1 || infos[0].Kind != providers[retained].kind {
				t.Fatalf("clear did not preserve other slot: %+v %v", infos, err)
			}
			callers, err := s.store.callers()
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range callers {
				switch c.ID {
				case mixed.ID:
					if c.Revoked || !slices.Equal(c.Grants, []string{routes[retained].Name}) {
						t.Fatalf("mixed client lost unrelated access: %+v", c)
					}
				case devices[removed].CallerID:
					if !c.Revoked || len(c.Grants) != 0 {
						t.Fatalf("removed provider client still active: %+v", c)
					}
				case devices[retained].CallerID:
					if c.Revoked || !slices.Equal(c.Grants, []string{routes[retained].Name}) {
						t.Fatalf("other provider client lost access: %+v", c)
					}
				}
			}
			if _, err := s.store.authorize(token, s.snap.routes[0]); err != nil {
				t.Fatal("mixed token no longer authorizes retained route", err)
			}
			for i, ar := range routes {
				want := 0
				if i == retained {
					want = 1
				}
				if n := proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE account=? AND caller=?", ar.Account, devices[i].CallerID); n != want {
					t.Fatalf("%s enrollment count = %d, want %d", providers[i].kind, n, want)
				}
			}
			a, err := s.store.account(routes[retained].Account)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := claudeSharedSecret([32]byte{byte(retained + 1)}, a)
			if err != nil || a.State != "ready" || secret.Access != "synthetic-access" {
				t.Fatalf("other provider's shared account no longer works: %v", err)
			}
		})
	}
}

func markNativeSetupAccountReady(t *testing.T, s *Service, name string, key [32]byte) {
	t.Helper()
	a, err := s.store.account(name)
	if err != nil {
		t.Fatal(err)
	}
	next := a
	next.Generation++
	if err := sealNativeAccount(key, &next, claudeVaultReply{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Scope: claudeTestScope, ExpiresIn: 3600}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.changeClaudeSharedAccount(context.Background(), a, next); err != nil {
		t.Fatal(err)
	}
}
