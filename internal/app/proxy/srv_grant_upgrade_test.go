package proxy

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	srvUpgradeFirstOrigin  = "https://first.example.test:8443"
	srvUpgradeSecondOrigin = "https://second.example.test:9443"
	srvUpgradeStatic       = "https://static.example.test:443"
)

func TestSRVGrantUpgradeOnDBStartup(t *testing.T) {
	for _, shape := range []string{"single", "composite"} {
		for _, tc := range []struct {
			name, oldOrigin, restartOrigin, account string
			migrate                                 bool
		}{
			{name: "normalized", oldOrigin: "srv://_inference._tcp.example.test", restartOrigin: srvUpgradeSecondOrigin, migrate: true},
			{name: "concrete", oldOrigin: srvUpgradeFirstOrigin, restartOrigin: srvUpgradeFirstOrigin},
			{name: "concrete-with-account", oldOrigin: srvUpgradeFirstOrigin, restartOrigin: srvUpgradeFirstOrigin, account: "plan"},
			// 8125a1fc never emitted a normalized scope for an account-backed
			// route. A row with that synthetic scope must not be accepted either.
			{name: "normalized-with-account", oldOrigin: "srv://_inference._tcp.example.test", restartOrigin: srvUpgradeFirstOrigin, account: "plan"},
		} {
			t.Run(shape+"/"+tc.name, func(t *testing.T) {
				testSRVGrantUpgrade(t, shape == "composite", tc.oldOrigin, tc.restartOrigin, tc.account, tc.migrate)
			})
		}
	}
}

func testSRVGrantUpgrade(t *testing.T, composite bool, oldOrigin, restartOrigin, account string, migrate bool) {
	const upstream = "srv://_Inference._tcp.Example.TEST/v1%2bpreview"
	const storedKey = "fixture-upstream-key"
	const base = "/v1%2bpreview"
	resolved, lookups := srvUpgradeFirstOrigin+base, 0
	stubUpstreamSRV(t, func(ref string) (string, error) {
		if ref != upstream {
			t.Fatalf("SRV lookup got %q, want configured reference %q", ref, upstream)
		}
		lookups++
		return resolved, nil
	})

	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	if w := adminRequest(s, http.MethodPut, "secrets", map[string]string{"name": "key", "value": storedKey}, cookie, csrf); w.Code != http.StatusOK {
		t.Fatalf("store API key: %d %s", w.Code, w.Body)
	}
	if account != "" {
		a := accountRecord{
			AccountInfo: AccountInfo{Name: account, Kind: "chatgpt", State: "ready", Generation: 1},
			operator:    accountOperator{Origins: []string{srvUpgradeFirstOrigin, srvUpgradeStatic}},
		}
		if err := s.store.replaceAccount(t.Context(), a, 0); err != nil {
			t.Fatal(err)
		}
	}
	r := Route{Name: "svc", LocalPrefix: "/svc/", Enabled: true, Upstream: upstream, Account: account,
		SetHeaders: map[string]HeaderRule{"X-Upstream-Key": {Secret: "key", Prefix: "Real "}}}
	paths := []string{"/svc/probe"}
	if composite {
		// Persist out of prefix order; the historical parent hash sorted by prefix.
		r.Rules = []RouteRule{
			{PathPrefix: "/named/", Upstream: r.Upstream, SetHeaders: r.SetHeaders},
			{PathPrefix: "/", Upstream: srvUpgradeStatic + "/fallback"},
		}
		r.Upstream, r.SetHeaders = "", nil
		paths = []string{"/svc/named/probe", "/svc/probe"}
	}
	w := httptest.NewRecorder()
	s.updateConfig(w, fixtureProviders(Config{Routes: []Route{r}}), s.snap.revision)
	if w.Code != http.StatusOK {
		t.Fatalf("save DB-managed route: %d %s", w.Code, w.Body)
	}
	client, token, err := s.store.createCaller("persisted client", []string{r.Name}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := srvUpgradeHistoricalHash(composite, oldOrigin, account)
	execProxySQL(t, s.store, "UPDATE grants SET authority=? WHERE caller=? AND route=?", oldHash, client.ID, r.Name)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	resolved = restartOrigin + base
	next, err := Open(s.opts)
	if err != nil {
		t.Fatalf("restart DB-managed proxy: %v", err)
	}
	t.Cleanup(func() { next.Close() })
	if selected := next.snap.selectRoute(paths[0]); selected == nil || selected.target.String() != resolved || lookups != 2 {
		t.Fatalf("restart did not pin the expected SRV answer %q (lookups: %d)", resolved, lookups)
	} else if selected.headers.Get("X-Upstream-Key") != "Real "+storedKey {
		t.Fatal("restarted route did not load its stored API key")
	}

	var storedHash string
	err = next.store.db.QueryRow("SELECT authority FROM grants WHERE caller=? AND route=?", client.ID, r.Name).Scan(&storedHash)
	if migrate {
		if want := next.snap.routes[0].authority; err != nil || storedHash == oldHash || storedHash != want {
			t.Errorf("persisted grant authority = %q, err = %v; want rewritten authority %q (old %q)", storedHash, err, want, oldHash)
		}
	} else {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("incompatible grant was not removed: authority = %q, err = %v", storedHash, err)
		}
		if got, err := next.store.callerForToken(token); err != nil || got.ID != client.ID {
			t.Fatalf("removing the grant invalidated the client token: %v", err)
		}
	}
	for _, path := range paths {
		selected := next.snap.selectRoute(path)
		if selected == nil {
			t.Fatalf("route did not match %s after restart", path)
		}
		got, err := next.store.authorize(token, *selected)
		if migrate && (err != nil || got.ID != client.ID) {
			t.Errorf("original token no longer authorizes %s: %v", path, err)
		} else if !migrate && !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("original token should have no grant for %s, got %v", path, err)
		}
	}
}

// Literal scope JSON from 8125a1fc's config.go and route_rules.go formats.
// The concrete fixture uses the DNS answer as Origin, as the older encoding
// did, while the historical normalized fixture uses the lowercased SRV host.
func srvUpgradeHistoricalHash(composite bool, origin, account string) string {
	accountField := ""
	if account != "" {
		accountField = fmt.Sprintf(`,"Account":%q`, account)
	}
	srv := fmt.Sprintf(`{"Origin":%q,"Base":"/v1%%2Bpreview/","Rules":[{"Header":"x-upstream-key","Secret":"key","Prefix":"Real ","Value":null}]%s}`, origin, accountField)
	if !composite {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(srv)))
	}
	fallback := fmt.Sprintf(`{"Origin":%q,"Base":"/fallback/","Rules":[]%s}`, srvUpgradeStatic, accountField)
	parent := fmt.Sprintf(`{"Rules":[{"PathPrefix":"/","Authority":"%x"},{"PathPrefix":"/named/","Authority":"%x"}],"Redirects":"passthrough"}`,
		sha256.Sum256([]byte(fallback)), sha256.Sum256([]byte(srv)))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(parent)))
}
