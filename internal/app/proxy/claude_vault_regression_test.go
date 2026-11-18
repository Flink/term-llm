package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClaudeVaultRetryLifetimeAndCurrentSuccessor(t *testing.T) {
	for _, state := range []string{"ready", "expired", "refreshing", "spent", "missing"} {
		t.Run(state, func(t *testing.T) {
			f := newClaudeForwardFixture(t)
			result := claudeVaultDecode(t, claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh)))
			expires := time.Now().Unix() + 17
			switch state {
			case "expired":
				expires = time.Now().Unix() - 1
			case "missing":
				if _, err := f.service.store.db.Exec("DELETE FROM claude_vault WHERE access_hash=?", hashToken(result.AccessToken)); err != nil {
					t.Fatal(err)
				}
			}
			if state != "missing" {
				nextState := state
				if state == "expired" {
					nextState = "ready"
				}
				if _, err := f.service.store.db.Exec("UPDATE claude_vault SET state=?,expires=? WHERE access_hash=?", nextState, expires, hashToken(result.AccessToken)); err != nil {
					t.Fatal(err)
				}
			}
			retry := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
			if state == "ready" {
				reply := claudeVaultDecode(t, retry)
				if reply.AccessToken != result.AccessToken || reply.RefreshToken != result.RefreshToken || reply.ExpiresIn <= 0 || reply.ExpiresIn > 17 {
					t.Fatal("retry returned stale full lifetime", reply.ExpiresIn)
				}
			} else if retry.Code == 200 {
				t.Fatal("dead successor resurrected", state)
			}
			if f.oauthCalls.Load() != 1 {
				t.Fatal("retry dispatched to issuer")
			}
		})
	}
}

func TestClaudeVaultNativePlaceholdersCannotCrossCallerOrRoute(t *testing.T) {
	f := newClaudeForwardFixtureWithConcurrency(t, 8)
	otherCaller := caller(t, f.service, "claude")
	conn, reader := f.nativeHost(t, claudeAPIHost, otherCaller)
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, f.access, ""); code != 401 {
		t.Fatal("OAuth placeholder crossed caller", code)
	}
	f.service.mu.RLock()
	otherRoute := f.service.snap.routes[0].Route
	f.service.mu.RUnlock()
	otherRoute.Name = "other"
	otherRoute.LocalPrefix = "/other/"
	configure(t, f.service, Config{Routes: []Route{f.service.snap.routes[0].Route, otherRoute}}, nil)
	both := caller(t, f.service, "claude", "other")
	scope, key := claudeVaultTestScope(t, f)
	// A fresh mapping belonging to the multi-route caller still cannot move to
	// another route backed by the same account and same official upstream.
	original, principal, err := f.service.claudeForwardRouteLocked("claude", both)
	if err != nil {
		t.Fatal(err)
	}
	scope.caller = principal.ID
	reply, err := f.service.store.claudeVaultSave(context.Background(), key, scope, claudeVaultReply{AccessToken: "real-access", RefreshToken: "real-refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: "user:profile user:inference"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	forwarder, err := f.service.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Host = claudeAPIHost
	req.Header.Set("Authorization", "Bearer "+reply.AccessToken)
	rr := httptest.NewRecorder()
	f.service.claudeForwardNative(rr, req, &claudeNativeTunnel{forwarder: forwarder, route: "other", token: both, host: claudeAPIHost})
	if rr.Code != 401 || f.vendorCalls.Load() != 0 {
		t.Fatal("OAuth placeholder crossed route", rr.Code, original.Name)
	}
}

func TestClaudeVaultAccountReplacementAndGrantChurnInvalidate(t *testing.T) {
	for _, kind := range []string{"identity", "grant", "caller"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeForwardFixture(t)
			scope, key := claudeVaultTestScope(t, f)
			switch kind {
			case "identity":
				account, err := f.service.store.account("claude")
				if err != nil {
					t.Fatal(err)
				}
				previous := account.Generation
				account.Generation++
				account.identityVersion++
				if err := f.service.store.replaceAccount(context.Background(), account, previous); err != nil {
					t.Fatal(err)
				}
			case "grant":
				if err := f.service.store.updateGrants(scope.caller, nil, f.service.snap); err != nil {
					t.Fatal(err)
				}
				if err := f.service.store.updateGrants(scope.caller, []string{"claude"}, f.service.snap); err != nil {
					t.Fatal(err)
				}
			case "caller":
				if err := f.service.store.revokeCaller(context.Background(), scope.caller); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.store.claudeVaultAccess(context.Background(), key, scope); err == nil {
				t.Fatal("old access survived mutation", kind)
			}
			if _, err := f.service.store.claudeVaultLookup(context.Background(), key, scope, f.refresh, true); err == nil {
				t.Fatal("old refresh survived mutation", kind)
			}
		})
	}
}

func TestClaudeVaultRefreshCustodyCommitFailureCannotReplay(t *testing.T) {
	f := newClaudeForwardFixture(t)
	if _, err := f.service.store.db.Exec(`CREATE TRIGGER refuse_vault_successor BEFORE INSERT ON claude_vault BEGIN SELECT RAISE(ABORT,'commit failed'); END`); err != nil {
		t.Fatal(err)
	}
	first := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	if first.Code == 200 || strings.Contains(first.Body.String(), "synthetic-vendor") {
		t.Fatal("uncommitted issuer tokens returned")
	}
	if _, err := f.service.store.db.Exec("DROP TRIGGER refuse_vault_successor"); err != nil {
		t.Fatal(err)
	}
	retry := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	if retry.Code == 200 || f.oauthCalls.Load() != 1 {
		t.Fatal("issuer refresh replayed after custody commit failure")
	}
}

func TestClaudeVaultFormRefreshAndTokenNamespace(t *testing.T) {
	f := newClaudeForwardFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "synthetic-vendor-refresh" || r.Form.Get("client_id") != claudeClientID {
			t.Error("native form refresh not translated", err)
		}
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh-next","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	result := claudeVaultDecode(t, claudeVaultDirectContent(t, f, "grant_type=refresh_token&client_id="+claudeClientID+"&refresh_token="+f.refresh, "application/x-www-form-urlencoded"))
	rr := claudeVaultDirect(t, f, claudeVaultRefreshPayload(result.AccessToken))
	if rr.Code == 200 || f.oauthCalls.Load() != 1 {
		t.Fatal("access placeholder used as refresh token")
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

func TestClaudeVaultInFlightCodeCannotSurviveIdenticalRegrant(t *testing.T) {
	f := newClaudeForwardFixture(t)
	scope, _ := claudeVaultTestScope(t, f)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		var pending int
		if err := f.service.store.db.QueryRow("SELECT count(*) FROM claude_vault WHERE state='exchanging' AND caller=?", scope.caller).Scan(&pending); err != nil || pending != 1 {
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
	if err := f.service.store.db.QueryRow("SELECT count(*) FROM claude_vault WHERE caller=?", scope.caller).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("late code recreated revoked vault rows", rows, err)
	}
}

func TestClaudeVaultCodeReservationMustCommitBeforeDispatch(t *testing.T) {
	f := newClaudeForwardFixture(t)
	if _, err := f.service.store.db.Exec(`CREATE TRIGGER refuse_code_reservation BEFORE INSERT ON claude_vault WHEN NEW.state='exchanging' BEGIN SELECT RAISE(ABORT,'reservation failed'); END`); err != nil {
		t.Fatal(err)
	}
	response := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if response.Code == 200 || f.oauthCalls.Load() != 0 {
		t.Fatal("code dispatched without a durable binding reservation")
	}
}
