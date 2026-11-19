package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeSharedLocalFormRefreshDoesNotReachIssuer(t *testing.T) {
	f := newClaudeForwardFixture(t)
	reply := claudeVaultDecode(t, claudeVaultDirectContent(t, f, "grant_type=refresh_token&client_id="+claudeClientID+"&refresh_token="+f.refresh, "application/x-www-form-urlencoded"))
	if reply.AccessToken != f.access || reply.RefreshToken != f.refresh || reply.Scope != claudeTestScope {
		t.Fatal("local refresh changed the scoped aliases or provider scopes")
	}
	for _, wrong := range []string{reply.AccessToken, "synthetic-vendor-refresh", f.token} {
		if rr := claudeVaultDirect(t, f, claudeVaultRefreshPayload(wrong)); rr.Code == 200 {
			t.Fatal("accepted a non-refresh alias")
		}
	}
	if f.oauthCalls.Load() != 0 {
		t.Fatal("local refresh contacted provider")
	}
}

func TestClaudeSharedAccessAliasCannotCrossRoutesToSameAccount(t *testing.T) {
	f := newClaudeForwardFixture(t)
	primary := f.service.snap.routes[0].Route
	sibling := primary
	sibling.Name, sibling.LocalPrefix = "sibling", "/sibling/"
	configure(t, f.service, Config{Routes: []Route{primary, sibling}}, nil)
	token := caller(t, f.service, primary.Name, sibling.Name)
	a, err := f.service.store.account(primary.Account)
	if err != nil {
		t.Fatal(err)
	}
	forwarder, err := f.service.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	alias := func(ar activeRoute) string {
		scope := claudeSharedTestCallerScope(t, f.service, ar, token)
		return claudeSharedAlias(forwarder.ca.vaultKey, a, scope, claudeProxyProvider.accessPrefix)
	}
	primaryAlias, siblingAlias := alias(f.service.snap.routes[0]), alias(f.service.snap.routes[1])
	conn, reader := f.nativeRouteHost(t, sibling.Name, claudeAPIHost, token)
	defer conn.Close()
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, primaryAlias, ""); code != http.StatusUnauthorized || f.vendorCalls.Load() != 0 {
		t.Fatalf("alias for first route authenticated on its sibling: HTTP %d", code)
	}
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, siblingAlias, ""); code != http.StatusOK || f.vendorCalls.Load() != 1 {
		t.Fatalf("legitimate alias did not authorize on its sibling: HTTP %d", code)
	}
}

func TestClaudeVaultWhitelistMetadataCannotEchoTokenViaScope(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"access_token": "real-access", "refresh_token": "real-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "user:profile user:inference real-access real-refresh", "nested": map[string]string{"token": "real-access"}, "id_token": "real-refresh"})
	reply, err := parseClaudeVaultReply(data)
	if err != nil {
		t.Fatal(err)
	}
	reply.AccessToken = "placeholder-access"
	reply.RefreshToken = "placeholder-refresh"
	sanitized, _ := json.Marshal(reply)
	if strings.Contains(string(sanitized), "real-access") || strings.Contains(string(sanitized), "real-refresh") || strings.Contains(string(sanitized), "nested") {
		t.Fatal("unknown metadata leaked credentials")
	}
}

func TestClaudeSharedInFlightCodeCannotSurviveIdenticalRegrant(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	scope := claudeSharedTestCallerScope(t, f.service, f.service.snap.routes[0], f.token)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		var pending int
		if err := f.service.store.db.QueryRow("SELECT count(*) FROM claude_shared_enrollment WHERE reserved=1 AND caller=?", scope.caller).Scan(&pending); err != nil || pending != 1 {
			t.Error("code sent before reservation committed", pending, err)
		}
		if err := f.service.store.updateGrants(scope.caller, nil, f.service.snap); err != nil {
			t.Error(err)
		}
		if err := f.service.store.updateGrants(scope.caller, []string{"claude"}, f.service.snap); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"access_token":"real-access","refresh_token":"real-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	response := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if response.Code == 200 || strings.Contains(response.Body.String(), "real-access") {
		t.Fatal("late code completion resurrected revoked custody", response.Code)
	}
	var rows int
	if err := f.service.store.db.QueryRow("SELECT count(*) FROM claude_shared_enrollment WHERE caller=?", scope.caller).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("late code recreated revoked enrollment", rows, err)
	}
}

func TestClaudeSharedCodeReservationMustCommitBeforeDispatch(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	if _, err := f.service.store.db.Exec(`CREATE TRIGGER refuse_code_reservation BEFORE UPDATE OF reserved ON claude_shared_enrollment WHEN NEW.reserved=1 BEGIN SELECT RAISE(ABORT,'reservation failed'); END`); err != nil {
		t.Fatal(err)
	}
	response := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if response.Code == 200 || f.oauthCalls.Load() != 0 {
		t.Fatal("code dispatched without a durable binding reservation")
	}
}
