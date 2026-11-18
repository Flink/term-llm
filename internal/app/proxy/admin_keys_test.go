package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type issuedAdminKey struct {
	Key   AdminKey `json:"key"`
	Token string   `json:"token"`
}

func proxyResponse[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body)
	}
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return out
}

func issueAdminKey(t *testing.T, s *Service, cookie *http.Cookie, csrf, name string) issuedAdminKey {
	t.Helper()
	out := proxyResponse[issuedAdminKey](t, adminRequest(s, "POST", "admin-keys", map[string]string{"name": name}, cookie, csrf), 201)
	if !strings.HasPrefix(out.Token, adminKeyPrefix) || !strings.HasPrefix(out.Key.ID, "ak_") || len(out.Key.ID) != 26 || out.Key.LastUsed != 0 {
		t.Fatal("invalid issuance metadata")
	}
	return out
}

func TestBrowserAdminKeysTokenSession(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	body := map[string]string{"name": "Provisioner"}
	proxyResponse[map[string]string](t, adminRequest(s, "POST", "admin-keys", body, cookie, ""), 403)
	proxyResponse[map[string]string](t, adminRequest(s, "POST", "admin-keys", map[string]string{"name": " "}, cookie, csrf), 400)
	issued := issueAdminKey(t, s, cookie, csrf, "  Provisioner ")
	var hash string
	if err := s.store.db.QueryRow("SELECT hash FROM admin_keys WHERE id=?", issued.Key.ID).Scan(&hash); err != nil || hash != hashToken(issued.Token) {
		t.Fatal("stored key is not hash-only", err)
	}
	state := adminRequest(s, "GET", "state", nil, cookie, csrf)
	out := proxyResponse[struct {
		AdminKeys []AdminKey `json:"admin_keys"`
	}](t, state, 200)
	if len(out.AdminKeys) != 1 || out.AdminKeys[0].Name != "Provisioner" || strings.Contains(state.Body.String(), issued.Token) || strings.Contains(state.Body.String(), hash) {
		t.Fatal("unexpected key metadata exposure")
	}
	for range maxAdminKeys - 1 {
		issueAdminKey(t, s, cookie, csrf, "Another key")
	}
	proxyResponse[map[string]string](t, adminRequest(s, "POST", "admin-keys", body, cookie, csrf), 409)
	client := createMachineClient(t, s, issued.Token, "Keep client", nil, "")
	revoke := map[string]string{"id": issued.Key.ID}
	deleted := proxyResponse[struct {
		OK             bool
		RevokedClients int `json:"revoked_clients"`
	}](t, adminRequest(s, "DELETE", "admin-keys", revoke, cookie, csrf), 200)
	if !deleted.OK || deleted.RevokedClients != 0 {
		t.Fatalf("unexpected revoke: %+v", deleted)
	}
	proxyResponse[map[string]string](t, adminRequest(s, "DELETE", "admin-keys", revoke, cookie, csrf), 404)
	proxyResponse[map[string]string](t, manageRequest(s, "GET", "clients", issued.Token, nil), 401)
	fresh := issueAdminKey(t, s, cookie, csrf, "Replacement key")
	result := proxyResponse[struct{ Client Caller }](t, manageRequest(s, "GET", "clients/"+client.Client.ID, fresh.Token, nil), 200)
	if result.Client.Revoked || result.Client.CreatedBy != issued.Key.ID {
		t.Fatal("non-cascade revoke affected existing client or attribution")
	}
}

func TestBrowserAdminKeysRequireRecentPasskey(t *testing.T) {
	s, _ := passkeyFixture(t)
	browser := newPasskeyBrowser(t, s)
	_, assert := bootstrapPasskey(t, browser)
	body := map[string]string{"name": "Passkey provisioner"}
	browser.send("POST", "admin-keys", body, 403)
	assert("reauth", 1)
	issued := proxyResponse[issuedAdminKey](t, browser.send("POST", "admin-keys", body, 201), 201)
	revoke := map[string]string{"id": issued.Key.ID}
	browser.send("DELETE", "admin-keys", revoke, 403)
	assert("reauth", 2)
	browser.send("DELETE", "admin-keys", revoke, 200)
	browser.send("POST", "admin-keys", body, 403)
}

func TestBrowserAdminKeyCascadeIsSelectiveAndAtomic(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Cascade me")
	other := issueAdminKey(t, s, cookie, csrf, "Keep me")
	a := createMachineClient(t, s, key.Token, "A", nil, "").Client
	b := createMachineClient(t, s, key.Token, "B", nil, "").Client
	old := createMachineClient(t, s, key.Token, "Already revoked", nil, "").Client
	keep := createMachineClient(t, s, other.Token, "Unrelated", nil, "").Client
	manual, _, err := s.store.createCaller("Browser", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	proxyResponse[map[string]bool](t, manageRequest(s, "DELETE", "clients/"+old.ID, key.Token, nil), 200)
	for _, c := range []Caller{a, b, keep, manual} {
		seedManageVault(t, s, c.ID, "fixture")
	}
	revokedSocket := fakeManageSocket(t, s, a.ID)
	keptSocket := fakeManageSocket(t, s, keep.ID)
	execProxySQL(t, s.store, `CREATE TRIGGER fail_cascade BEFORE DELETE ON claude_vault WHEN OLD.caller = '`+b.ID+`' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END;`)
	revoke := map[string]any{"id": key.Key.ID, "revoke_clients": true}
	proxyResponse[map[string]string](t, adminRequest(s, "DELETE", "admin-keys", revoke, cookie, csrf), 503)
	proxyResponse[map[string]any](t, manageRequest(s, "GET", "clients", key.Token, nil), 200)
	if proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE id IN (?,?) AND revoked=0", a.ID, b.ID) != 2 || proxyCount(t, s.store, "SELECT count(*) FROM claude_vault") != 4 || revokedSocket.Err() != nil {
		t.Fatal("failed cascade changed access")
	}
	execProxySQL(t, s.store, "DROP TRIGGER fail_cascade")
	result := proxyResponse[struct {
		RevokedClients int `json:"revoked_clients"`
	}](t, adminRequest(s, "DELETE", "admin-keys", revoke, cookie, csrf), 200)
	if result.RevokedClients != 2 || proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE revoked=0") != 2 || proxyCount(t, s.store, "SELECT count(*) FROM claude_vault") != 2 || !errors.Is(context.Cause(revokedSocket), errSocketRevoked) || keptSocket.Err() != nil {
		t.Fatalf("cascade overreached or failed: %+v", result)
	}
}

func TestAdminKeyAuditDoesNotLogProxyCredentials(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	var audit bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&audit)
	t.Cleanup(func() { log.SetOutput(previous) })
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	client := createMachineClient(t, s, key.Token, "x"+key.Token, nil, "")
	nested := issueAdminKey(t, s, cookie, csrf, client.Token)
	proxyResponse[map[string]any](t, adminRequest(s, "DELETE", "admin-keys", map[string]any{"id": key.Key.ID, "revoke_clients": true}, cookie, csrf), 200)
	out := audit.String()
	for _, token := range []string{key.Token, client.Token, nested.Token} {
		if strings.Contains(out, token) {
			t.Fatal("audit contains a proxy credential")
		}
	}
	for _, field := range []string{"admin_key_issue", "manage_client_create", "admin_key_revoke", "cascade=1", key.Key.ID, client.Client.ID, `name="Automation"`, `name="[redacted]"`} {
		if !strings.Contains(out, field) {
			t.Errorf("audit lacks %s", field)
		}
	}
}
