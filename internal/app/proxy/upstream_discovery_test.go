package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func stubUpstreamSRV(t *testing.T, resolve func(string) (string, error)) {
	t.Helper()
	old := resolveUpstreamSRV
	t.Cleanup(func() { resolveUpstreamSRV = old })
	resolveUpstreamSRV = resolve
}

func TestSRVUpstreamForwardsTLSWithBoundCredential(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/base%20path/id%2Fpart" || r.URL.RawQuery != "q=a%2Fb" {
			t.Errorf("unexpected target: %s?%s", got, r.URL.RawQuery)
		}
		if r.Header.Get("X-Upstream-Key") != "Real bound-key" || r.Header.Get("X-Local") != "" {
			t.Error("upstream did not get only its bound credential")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	const raw = "srv://_inference._tcp.example.test/base%20path/"
	lookups := 0
	stubUpstreamSRV(t, func(ref string) (string, error) {
		lookups++
		if ref != raw {
			t.Fatalf("unexpected discovery reference: %q", ref)
		}
		return up.URL + "/base%20path/", nil
	})
	s := testService(t)
	s.transport.TLSClientConfig = up.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	for _, useRules := range []bool{false, true} {
		name, prefix := "single", "/single/"
		if useRules {
			name, prefix = "rules", "/rules/"
		}
		r := route(name, prefix, raw, "X-Local", "key")
		r.AllowHTTP = false
		target := prefix + "id%2Fpart?q=a%2Fb"
		if useRules {
			r.Rules = []RouteRule{{PathPrefix: "/named/", Upstream: raw, CallerAuth: r.CallerAuth, SetHeaders: r.SetHeaders}}
			r.Upstream, r.CallerAuth, r.SetHeaders = "", Auth{}, nil
			target = prefix + "named/id%2Fpart?q=a%2Fb"
		}
		configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "bound-key")})
		token := caller(t, s, name)
		if rr := call(s, http.MethodGet, target, "X-Local", token, nil); rr.Code != http.StatusNoContent {
			t.Fatalf("%s returned %d: %s", name, rr.Code, rr.Body)
		}
		stored, _, _, err := s.store.load()
		if err != nil {
			t.Fatal(err)
		}
		saved := stored.Routes[0].Upstream
		if useRules {
			saved = stored.Routes[0].Rules[0].Upstream
		}
		if saved != raw {
			t.Errorf("persisted destination = %q, want original SRV reference", saved)
		}
	}
	if lookups != 2 {
		t.Fatalf("expected one resolution per activation and none while forwarding; got %d", lookups)
	}
}

func TestSRVUpstreamRecompileKeepsPinnedOriginButRediscoveryChangesAuthority(t *testing.T) {
	const raw = "srv://_inference._tcp.example.test/v1"
	const endpoint = "https://api.example.test:8443"
	calls := 0
	resolved := endpoint + "/v1"
	stubUpstreamSRV(t, func(string) (string, error) { calls++; return resolved, nil })
	c := Config{Routes: []Route{route("svc", "/svc/", raw, "X-Local", "key")}}
	secrets := map[string]material{"key": boundMaterial(t, endpoint, "old-key")}
	first, err := compile(fixtureProviders(c), 1, secrets)
	if err != nil {
		t.Fatal(err)
	}
	resolved = "https://api.example.test:9443/v1"
	secrets = map[string]material{"key": boundMaterial(t, endpoint, "rotated-key")}
	next, err := first.recompile(fixtureProviders(c), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || next.routes[0].target.String() != endpoint+"/v1" || next.routes[0].authority != first.routes[0].authority || next.routes[0].headers.Get("X-Upstream-Key") != "Real rotated-key" {
		t.Fatal("secret rotation did not retain the exact pinned upstream and scope")
	}
	changed, err := compile(fixtureProviders(c), 2, secrets)
	if err != nil || changed.routes[0].authority == first.routes[0].authority {
		t.Fatalf("rediscovery must change authority for the new port: %v", err)
	}
}

func TestSRVUpstreamReloadReconcilesGrantsAndKeepsLastGoodSnapshot(t *testing.T) {
	s := testService(t)
	s.opts.ConfigFile = filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(s.opts.ConfigFile, []byte(`providers: {svc: {label: Test}}
routes:
  - name: svc
    provider: svc
    local_prefix: /svc/
    upstream: srv://_inference._tcp.example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved := "https://first.example.test:8443/v1"
	var lookupErr error
	stubUpstreamSRV(t, func(string) (string, error) { return resolved, lookupErr })
	reload := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.reload(w)
		return w
	}
	if w := reload(); w.Code != http.StatusOK {
		t.Fatalf("initial reload: %d %s", w.Code, w.Body)
	}
	token := caller(t, s, "svc")
	old := s.snap
	lookupErr = errors.New("SRV unavailable")
	if w := reload(); w.Code != http.StatusBadRequest || s.snap != old {
		t.Fatalf("failed reload must leave old snapshot active: %d %s", w.Code, w.Body)
	}
	if _, err := s.store.authorize(token, old.routes[0]); err != nil {
		t.Fatalf("failed reload changed grants: %v", err)
	}
	lookupErr = nil
	resolved = "https://second.example.test:8443/v1"
	if w := reload(); w.Code != http.StatusOK || s.snap.routes[0].target.String() != resolved {
		t.Fatalf("reload did not switch to the newly discovered target: %d %s", w.Code, w.Body)
	}
	for _, snap := range []*snapshot{old, s.snap} {
		if _, err := s.store.authorize(token, snap.routes[0]); err == nil {
			t.Fatal("grant for the old SRV origin survived the change")
		}
	}
}
