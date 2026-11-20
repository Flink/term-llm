package proxy

import "testing"

// An administrator fixing a route's upstream must not silently revoke the
// clients already approved for it; deleting the route still does.
func TestAdminRouteEditKeepsGrants(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	r := Route{Name: "frontier", LocalPrefix: "/frontier/", Upstream: "https://upstream.example/v1/chat/completions", Enabled: true}
	if w := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{r}}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, token, err := s.store.createCaller("dev", []string{"frontier"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	before := s.snap.routes[0].authority

	r.Upstream = "https://upstream.example/v1"
	if w := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{r}}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.snap.routes[0].authority == before {
		t.Fatal("upstream edit did not change the route authority; test is not exercising rebinding")
	}
	if _, err := s.store.authorize(token, s.snap.routes[0]); err != nil {
		t.Fatal("route edit dropped the client's grant:", err)
	}

	if w := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{}}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if n := proxyCount(t, s.store, "SELECT count(*) FROM grants"); n != 0 {
		t.Fatalf("deleted route kept %d grants", n)
	}
}
