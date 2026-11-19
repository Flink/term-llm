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

const claudeTestScope = "user:profile user:inference"

func seedReadyClaudeShared(t *testing.T, s *Service, name, access, refresh string) (accountRecord, [32]byte) {
	t.Helper()
	forwarder, err := s.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	a := newNativeAccount(name, claudeProxyProvider.kind)
	key := forwarder.ca.vaultKey
	if err := sealNativeAccount(key, &a, claudeVaultReply{AccessToken: access, RefreshToken: refresh, Scope: claudeTestScope, ExpiresIn: 3600}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	return a, key
}

func claudeSharedTestCallerScope(t *testing.T, s *Service, ar activeRoute, token string) claudeForwardScope {
	t.Helper()
	principal, err := s.store.authorize(token, ar)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.store.account(ar.Account)
	if err != nil {
		t.Fatal(err)
	}
	return claudeForwardScope{route: ar.Name, authority: ar.authority, account: ar.Account, caller: principal.ID, identity: a.identityVersion, provider: nativeProviderForKind(a.Kind)}
}

func prepareClaudeSharedEnrollment(t *testing.T, f *claudeForwardFixture) {
	t.Helper()
	s := f.service
	scope := claudeSharedTestCallerScope(t, s, s.snap.routes[0], f.token)
	a, err := s.store.account(scope.account)
	if err != nil {
		t.Fatal(err)
	}
	a.State, a.ExpiresAt, a.claudeSharedCipher = "needs_login", 0, nil
	a.Generation++
	if err := s.store.replaceAccount(context.Background(), a, a.Generation-1); err != nil {
		t.Fatal(err)
	}
	execProxySQL(t, s.store, `INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires) VALUES(?,?,?,?,?)`, scope.account, scope.caller, scope.route, scope.authority, time.Now().Add(claudeSharedEnrollmentTTL).Unix())
}

const claudeVaultCodePayload = `{"grant_type":"authorization_code","client_id":"` + claudeClientID + `","code":"synthetic-code","code_verifier":"synthetic-verifier","redirect_uri":"http://localhost:1234/callback"}`

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

func TestClaudeVaultRejectsConsoleAndBoundsIssuerBodies(t *testing.T) {
	for _, scope := range []string{"org:create_api_key user:profile", "user:profile", "user:inference", "user:profile user:inference org:create_api_key", ""} {
		data, _ := json.Marshal(claudeVaultReply{AccessToken: "real-access", RefreshToken: "real-refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: scope})
		if _, err := parseClaudeVaultReply(data); err == nil {
			t.Errorf("non-subscription scope accepted: %q", scope)
		}
	}
	f := newClaudeForwardProbeFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", claudeVaultMaxBody+1))
	}
	rr := claudeVaultDirect(t, f, claudeVaultCodePayload)
	if rr.Code == 200 || len(rr.Body.Bytes()) > 1024 || f.oauthCalls.Load() != 1 {
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
