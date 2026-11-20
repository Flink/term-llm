package proxy

import (
	"net/http"
	"reflect"
	"testing"
)

// Shared sign-in arms the chosen client instead of minting a launcher client.
func TestNativeSharedSignInArmsExistingClient(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	dev, token, err := s.store.createCaller("dev", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	setup := func(in map[string]any) nativeSetupReply {
		t.Helper()
		in["revision"] = s.snap.revision
		return proxyResponse[nativeSetupReply](t, adminRequest(s, http.MethodPost, "claude/setup", in, cookie, csrf), http.StatusCreated)
	}
	created := setup(map[string]any{"shared_action": "create", "caller": dev.ID})
	if created.CallerID != dev.ID || created.Token != "" || created.URL == "" {
		t.Fatalf("existing client reply = %+v", created)
	}
	route := s.snap.routes[0]
	assertOnlyClient := func() {
		t.Helper()
		clients, err := s.store.callers()
		if err != nil || len(clients) != 1 || clients[0].ID != dev.ID || clients[0].Revoked || !reflect.DeepEqual(clients[0].Grants, []string{route.Name}) {
			t.Fatalf("clients = %+v, err = %v", clients, err)
		}
		if n := proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE account=? AND caller=? AND route=?", route.Account, dev.ID, route.Name); n != 1 {
			t.Fatalf("enrollments for dev = %d", n)
		}
	}
	assertOnlyClient()
	if _, _, err := s.claudeForwardRouteLocked(route.Name, token); err != nil {
		t.Fatalf("armed client cannot use its route: %v", err)
	}
	reconnect := setup(map[string]any{"shared_action": "connect", "route": route.Name, "caller": dev.ID})
	if reconnect.CallerID != dev.ID || reconnect.Token != "" {
		t.Fatalf("reconnect reply = %+v", reconnect)
	}
	assertOnlyClient()

	named := setup(map[string]any{"shared_action": "connect", "route": route.Name, "name": "laptop"})
	if named.CallerID == dev.ID || named.Token == "" {
		t.Fatalf("new named client reply = %+v", named)
	}
	clients, err := s.store.callers()
	if err != nil || len(clients) != 2 {
		t.Fatalf("clients = %+v, err = %v", clients, err)
	}
	for _, c := range clients {
		if c.ID == named.CallerID && c.Name != "laptop" {
			t.Fatalf("new client name = %q", c.Name)
		}
	}
}

func TestNativeSharedSignInRejectsInvalidClientTarget(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	revoked, _, err := s.store.createCaller("old", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.revokeCaller(t.Context(), revoked.ID); err != nil {
		t.Fatal(err)
	}
	launch := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "create", "")
	route := s.snap.routes[0].Name
	before := proxyCount(t, s.store, "SELECT count(*) FROM callers")
	for name, in := range map[string]map[string]any{
		"revoked":         {"shared_action": "connect", "route": route, "caller": revoked.ID},
		"missing":         {"shared_action": "connect", "route": route, "caller": "c_missing"},
		"caller and name": {"shared_action": "connect", "route": route, "caller": launch.CallerID, "name": "x"},
		"device caller":   {"route": route, "caller": launch.CallerID},
	} {
		in["revision"] = s.snap.revision
		if rr := adminRequest(s, http.MethodPost, "claude/setup", in, cookie, csrf); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d %s", name, rr.Code, rr.Body)
		}
	}
	if after := proxyCount(t, s.store, "SELECT count(*) FROM callers"); after != before {
		t.Fatalf("rejected setup changed clients: %d -> %d", before, after)
	}
}
