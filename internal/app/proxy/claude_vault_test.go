package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const claudeVaultCodePayload = `{"grant_type":"authorization_code","client_id":"` + claudeClientID + `","code":"synthetic-code","code_verifier":"synthetic-verifier","redirect_uri":"http://localhost:1234/callback"}`

func claudeVaultTestScope(t *testing.T, f *claudeForwardFixture) (claudeForwardScope, [32]byte) {
	t.Helper()
	f.service.mu.RLock()
	ar, principal, err := f.service.claudeForwardRouteLocked("claude", f.token)
	f.service.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.service.store.account(ar.Account)
	if err != nil {
		t.Fatal(err)
	}
	forwarder, err := f.service.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	return claudeForwardScope{route: ar.Name, authority: ar.authority, account: ar.Account, caller: principal.ID, identity: account.identityVersion, access: f.access}, forwarder.ca.vaultKey
}
func claudeVaultDirect(t *testing.T, f *claudeForwardFixture, body string) *httptest.ResponseRecorder {
	t.Helper()
	return claudeVaultDirectContent(t, f, body, "application/json")
}
func claudeVaultDirectContent(t *testing.T, f *claudeForwardFixture, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	forwarder, err := f.service.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/oauth/token", strings.NewReader(body))
	req.Host = claudeOAuthHost
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	f.service.claudeForwardNative(rr, req, &claudeNativeTunnel{forwarder: forwarder, route: "claude", token: f.token, host: claudeOAuthHost})
	return rr
}
func claudeVaultRefreshPayload(token string) string {
	data, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": claudeClientID, "refresh_token": token})
	return string(data)
}
func claudeVaultDecode(t *testing.T, rr *httptest.ResponseRecorder) claudeVaultReply {
	t.Helper()
	if rr.Code != 200 {
		t.Fatalf("OAuth status %d: %s", rr.Code, rr.Body.String())
	}
	var reply claudeVaultReply
	if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.AccessToken, "sk-ant-oat01-tlv2-") || !strings.HasPrefix(reply.RefreshToken, "sk-ant-ort01-tlv2-") {
		t.Fatal("not opaque native OAuth tokens")
	}
	return reply
}

func TestClaudeVaultNativeCodeExchangeAndAPI(t *testing.T) {
	f := newClaudeForwardFixtureWithConcurrency(t, 8)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/oauth/token" || string(body) != claudeVaultCodePayload {
			t.Errorf("native authorization payload changed: %s", body)
		}
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh","token_type":"bearer","expires_in":3600,"scope":"user:profile user:inference","unknown_token":"must-not-escape","nested":{"refresh_token":"must-not-escape"}}`)
	}
	conn, reader := f.nativeHost(t, claudeOAuthHost, f.token)
	if _, err := fmt.Fprintf(conn, "POST /v1/oauth/token HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", claudeOAuthHost, len(claudeVaultCodePayload), claudeVaultCodePayload); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), "synthetic-vendor") || strings.Contains(string(body), "must-not-escape") {
		t.Fatal("unsafe native response", resp.StatusCode, string(body))
	}
	var reply claudeVaultReply
	if err = json.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	api, apiReader := f.native(t)
	if status, _ := claudeForwardNativeCall(t, api, apiReader, "GET", "/v1/models", claudeAPIHost, reply.AccessToken, ""); status != 200 {
		t.Fatal("placeholder API translation", status)
	}
	if status, _ := claudeForwardNativeCall(t, api, apiReader, "GET", "/v1/models", claudeAPIHost, f.token, ""); status != 401 {
		t.Fatal("caller token accepted as OAuth identity", status)
	}
	account, err := f.service.store.account("claude")
	if err != nil || account.credentials.AccessToken != "" || account.credentials.RefreshToken != "" {
		t.Fatal("native login overwrote shared account", err)
	}
	records, _, err := f.service.store.records(nil, 100)
	if err != nil || len(records) != 1 {
		t.Fatal("OAuth incorrectly entered inference ledger", len(records), err)
	}
	if f.oauthCalls.Load() != 1 || f.vendorCalls.Load() != 1 {
		t.Fatal("unexpected synthetic dispatch count")
	}
}

func TestClaudeVaultBindingAndEncryptedPersistence(t *testing.T) {
	f := newClaudeForwardFixture(t)
	scope, key := claudeVaultTestScope(t, f)
	for _, field := range []string{"caller", "route", "authority", "account", "identity"} {
		changed := scope
		switch field {
		case "caller":
			changed.caller = "different"
		case "route":
			changed.route = "different"
		case "authority":
			changed.authority = "different"
		case "account":
			changed.account = "different"
		case "identity":
			changed.identity++
		}
		if _, err := f.service.store.claudeVaultAccess(context.Background(), key, changed); err == nil {
			t.Error("placeholder escaped binding", field)
		}
	}
	var data []byte
	if err := f.service.store.db.QueryRow("SELECT data FROM claude_vault WHERE access_hash=?", hashToken(f.access)).Scan(&data); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-vendor-access", "synthetic-vendor-refresh", f.access, f.refresh} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext vault material")
		}
	}
	wrongKey := key
	wrongKey[0] ^= 1
	if _, err := f.service.store.claudeVaultAccess(context.Background(), wrongKey, scope); err == nil {
		t.Fatal("wrong sealing key accepted")
	}
	opts := f.service.opts
	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	forwarder, err := next.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	access, err := next.store.claudeVaultAccess(context.Background(), forwarder.ca.vaultKey, scope)
	if err != nil || access != "synthetic-vendor-access" {
		t.Fatal("vault mapping lost across service restart", err)
	}
}

func TestClaudeVaultRefreshRotationAndCachedRetry(t *testing.T) {
	f := newClaudeForwardFixtureWithConcurrency(t, 8)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["refresh_token"] != "synthetic-vendor-refresh" {
			t.Error("placeholder not translated to real refresh")
		}
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh-next","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	first := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	reply := claudeVaultDecode(t, first)
	if reply.AccessToken == f.access || reply.RefreshToken == f.refresh {
		t.Fatal("generation placeholders reused")
	}
	retry := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	cachedReply := claudeVaultDecode(t, retry)
	if cachedReply.AccessToken != reply.AccessToken || cachedReply.RefreshToken != reply.RefreshToken || cachedReply.ExpiresIn > reply.ExpiresIn || cachedReply.ExpiresIn <= 0 || f.oauthCalls.Load() != 1 {
		t.Fatal("refresh retry was not a cached sanitized result", retry.Code, retry.Body.String())
	}
	scope, key := claudeVaultTestScope(t, f)
	if _, err := f.service.store.claudeVaultAccess(context.Background(), key, scope); err == nil {
		t.Fatal("old access generation survived rotation")
	}
	scope.access = reply.AccessToken
	if access, err := f.service.store.claudeVaultAccess(context.Background(), key, scope); err != nil || access != "synthetic-vendor-access" {
		t.Fatal("rotated access unavailable", err)
	}
	old, err := f.service.store.claudeVaultLookup(context.Background(), key, scope, f.refresh, true)
	if err != nil || old.private.Access != "" || old.private.Refresh != "" || old.private.Result == nil {
		t.Fatal("spent generation retained real pair", err)
	}
	// Cached receipts are durable, not a process-local replay convenience.
	store, err := openStore(f.service.opts.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	cached, err := store.claudeVaultLookup(context.Background(), key, scope, f.refresh, true)
	if err != nil || cached.private.Result == nil || cached.private.Result.AccessToken != reply.AccessToken {
		t.Fatal("receipt not durable", err)
	}
}

func TestClaudeVaultUncertainRefreshIsNeverReplayed(t *testing.T) {
	for _, kind := range []string{"issuer error", "lost response", "malformed success", "Console success"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeForwardFixture(t)
			f.oauth = func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "lost response":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
				case "issuer error":
					w.Header().Set("Location", "https://private.invalid/secret")
					w.WriteHeader(500)
					io.WriteString(w, "synthetic-vendor-access synthetic-vendor-refresh")
				case "malformed success":
					io.WriteString(w, `{"access_token":"synthetic-vendor-access"}`)
				case "Console success":
					io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference org:create_api_key"}`)
				}
			}
			first := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
			if first.Code == 200 || strings.Contains(first.Body.String(), "synthetic-vendor") || first.Header().Get("Location") != "" {
				t.Fatal("unsafe issuer failure", first.Code, first.Body.String())
			}
			again := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
			if again.Code == 200 || f.oauthCalls.Load() != 1 {
				t.Fatal("uncertain rotating refresh replayed")
			}
			scope, key := claudeVaultTestScope(t, f)
			store, err := openStore(f.service.opts.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			record, err := store.claudeVaultLookup(context.Background(), key, scope, f.refresh, true)
			if err != nil || record.state != "refreshing" {
				t.Fatal("before-send fence not durable", err)
			}
		})
	}
}

func TestClaudeVaultConcurrentRefreshFencesBeforeSend(t *testing.T) {
	f := newClaudeForwardFixtureWithConcurrency(t, 8)
	started, release := make(chan struct{}), make(chan struct{})
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-next-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh)) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("synthetic issuer did not receive refresh")
	}
	second := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	close(release)
	first := <-done
	if first.Code != 200 || second.Code == 200 || f.oauthCalls.Load() != 1 {
		t.Fatal("refresh was sent twice", first.Code, second.Code)
	}
}

func TestClaudeVaultRejectsConsoleAndBoundsIssuerBodies(t *testing.T) {
	for _, scope := range []string{"org:create_api_key user:profile", "user:profile", "user:inference", "user:profile user:inference org:create_api_key", ""} {
		data, _ := json.Marshal(claudeVaultReply{AccessToken: "real-access", RefreshToken: "real-refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: scope})
		if _, err := parseClaudeVaultReply(data); err == nil {
			t.Errorf("non-subscription scope accepted: %q", scope)
		}
	}
	f := newClaudeForwardFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", claudeVaultMaxBody+1))
	}
	rr := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if rr.Code == 200 || len(rr.Body.Bytes()) > 1024 {
		t.Fatal("unbounded issuer response")
	}
	before := f.oauthCalls.Load()
	for _, payload := range []string{strings.Repeat("x", claudeVaultMaxBody+1), `{"grant_type":"authorization_code","grant_type":"refresh_token"}`, `{"grant_type":"refresh_token","client_id":"wrong","refresh_token":"real-refresh"}`} {
		rr = claudeVaultDirect(t, f, payload)
		if rr.Code == 200 {
			t.Fatal("invalid OAuth payload accepted")
		}
	}
	if f.oauthCalls.Load() != before {
		t.Fatal("invalid payload dispatched")
	}
}

func TestClaudeVaultFenceFailureRefusesNetwork(t *testing.T) {
	f := newClaudeForwardFixture(t)
	_, err := f.service.store.db.Exec(`CREATE TRIGGER refuse_vault_fence BEFORE UPDATE ON claude_vault WHEN NEW.state='refreshing' BEGIN SELECT RAISE(ABORT,'fence failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	rr := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	if rr.Code == 200 || f.oauthCalls.Load() != 0 {
		t.Fatal("refresh sent without durable fence")
	}
}

func TestClaudeVaultCodeCompletionCannotRecreateRevokedBinding(t *testing.T) {
	f := newClaudeForwardFixture(t)
	scope, _ := claudeVaultTestScope(t, f)
	var calls atomic.Int32
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := f.service.store.db.Exec("DELETE FROM grants WHERE caller=? AND route=?", scope.caller, scope.route); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	rr := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if rr.Code == 200 || calls.Load() != 1 {
		t.Fatal("late login survived grant revocation", rr.Code)
	}
}
