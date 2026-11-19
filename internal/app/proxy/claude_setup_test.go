package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Keep the Claude-specific entry point used by the managed-client capacity tests.
func (s *store) createClaudeSetup(ctx context.Context, next *snapshot, account *accountRecord, route, id, token string) error {
	for _, r := range next.routes {
		if r.Name == route {
			return s.createNativeSetup(ctx, next, account, r.Route, id, token, claudeSetup.callerName, false, []string{route})
		}
	}
	return fmt.Errorf("missing route")
}

func claudeSetupPost(t *testing.T, s *Service, cookie *http.Cookie, csrf string) nativeSetupReply {
	t.Helper()
	return nativeSetupPost(t, s, "claude/setup", cookie, csrf, "create", "")
}

func nativeSetupPost(t *testing.T, s *Service, endpoint string, cookie *http.Cookie, csrf, action, route string) nativeSetupReply {
	t.Helper()
	rr := adminRequest(s, "POST", endpoint, nativeSetupInput{Revision: &s.snap.revision, SharedAction: action, Route: route}, cookie, csrf)
	if rr.Code != 201 || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("setup: %d %s headers=%v", rr.Code, rr.Body.String(), rr.Header())
	}
	var out nativeSetupReply
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Revision != s.snap.revision || out.CallerID == "" || !strings.HasPrefix(out.Token, "sk-tlp-") {
		t.Fatalf("invalid reply: %+v", out)
	}
	return out
}

func claudeSetupAccount(t *testing.T, s *Service, name, kind string) accountRecord {
	t.Helper()
	a := accountRecord{AccountInfo: AccountInfo{Name: name, Kind: kind, State: "ready", Generation: 1, Updated: time.Now().UnixMilli()}, credentials: accountCredentials{AccessToken: "synthetic-" + name}}
	if kind == "claude-code" || kind == "agy-bin" {
		a = newNativeAccount(name, kind)
	}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	return a
}

func claudeSetupRoute(name, account string) Route {
	return Route{Name: name, Account: account, Enabled: true, LocalPrefix: "/" + name + "/", Upstream: "https://api.anthropic.com", CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "}, Billing: "plan", Usage: "auto"}
}

func TestClaudeSetupDefaultsToCreateAndConnectReusesRoute(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	first := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "", "")
	if len(s.snap.routes) != 1 || first.URL != "http://localhost"+s.snap.routes[0].LocalPrefix {
		t.Fatalf("unexpected setup: %+v routes=%+v", first, s.snap.routes)
	}
	route := s.snap.routes[0]
	preset, err := claudeSetup.preset()
	if err != nil {
		t.Fatal(err)
	}
	if route.Upstream != preset.Upstream || route.CallerAuth != preset.Auth || route.Billing != preset.Billing || route.Usage != preset.Usage || !route.Enabled {
		t.Fatalf("not canonical defaults: %+v preset=%+v", route, preset)
	}
	a, err := s.store.account(route.Account)
	if err != nil || a.Kind != "claude-code" || !a.claudeShared() || a.State != "needs_login" || len(a.claudeSharedCipher) != 0 {
		t.Fatalf("not a native slot: %+v err=%v", a, err)
	}
	second := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "connect", route.Name)
	if second.CallerID == first.CallerID || second.Token == first.Token || second.URL != first.URL || second.Revision != first.Revision+1 || len(s.snap.routes) != 1 {
		t.Fatalf("repeat should only create another connection: %+v %+v", first, second)
	}
	infos, err := s.store.accountInfos()
	if err != nil || len(infos) != 1 {
		t.Fatalf("duplicated slots: %+v %v", infos, err)
	}
	clients, err := s.store.callers()
	if err != nil || len(clients) != 2 {
		t.Fatalf("clients=%+v err=%v", clients, err)
	}
	for _, c := range clients {
		if c.Name != "Claude Code sign-in launcher" || c.Revoked || !reflect.DeepEqual(c.Grants, []string{route.Name}) {
			t.Fatalf("unscoped client: %+v", c)
		}
	}
	for _, token := range []string{first.Token, second.Token} {
		if _, _, err := s.claudeForwardRouteLocked(route.Name, token); err != nil {
			t.Fatalf("connection cannot use native route: %v", err)
		}
	}
	if n := proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE account=? AND caller=? AND reserved=0", a.Name, second.CallerID); n != 1 {
		t.Fatal("connect did not replace the pending operator enrollment")
	}
	state := adminRequest(s, "GET", "state", nil, cookie, csrf)
	if _, err := os.Stat(filepath.Join(s.opts.StateDir, claudeForwardCAFile)); !os.IsNotExist(err) {
		t.Fatalf("setup unexpectedly initialized native TLS custody: %v", err)
	}
	for _, secret := range []string{first.Token, second.Token} {
		if strings.Contains(state.Body.String(), secret) {
			t.Fatal("one-time token leaked by state")
		}
	}
	claudeSetupAssertMetadata(t, s, second, route.Name)
}

func TestClaudeSetupConnectKeepsCustomizations(t *testing.T) {
	s := testService(t)
	claudeSetupAccount(t, s, "my-slot", "claude-code")
	r := claudeSetupRoute("custom-claude", "my-slot")
	r.Upstream += "/"
	r.Billing, r.Usage, r.Redirects = "unspecified", "off", "refuse"
	r.CallerAuth = Auth{Header: "authorization", Prefix: "Bearer "}
	r.Allow = []string{"GET /api/oauth/profile", "GET /api/oauth/usage", "GET /v1/models", "POST /v1/messages", "POST /v1/messages/count_tokens"}
	r.RequestIDHeader = "X-Custom-Request"
	configure(t, s, Config{Routes: []Route{r}}, nil)
	before := claudeSetupDBState(t, s, "accounts")
	cookie, csrf := loginAdmin(t, s)
	out := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "connect", r.Name)
	if out.URL != "http://localhost/custom-claude/" || len(s.snap.routes) != 1 || !reflect.DeepEqual(s.snap.config.Routes[0], r) {
		t.Fatalf("custom route overwritten: %+v want=%+v", s.snap.config.Routes, r)
	}
	if got := claudeSetupDBState(t, s, "accounts"); got != before {
		t.Fatal("existing account changed")
	}
	claudeSetupAssertMetadata(t, s, out, r.Name)
}

func TestClaudeSetupDeviceTargetsSelectedRoute(t *testing.T) {
	s := testService(t)
	seedReadyClaudeShared(t, s, "slot", "synthetic-access", "synthetic-refresh")
	first := claudeSetupRoute("first", "slot")
	second := claudeSetupRoute("second", "slot")
	disabled := claudeSetupRoute("disabled", "slot")
	disabled.Enabled = false
	configure(t, s, Config{Routes: []Route{first, second, disabled}}, nil)
	cookie, csrf := loginAdmin(t, s)
	for _, target := range []string{"disabled", "missing"} {
		rr := adminRequest(s, "POST", "claude/setup", map[string]any{"revision": s.snap.revision, "route": target}, cookie, csrf)
		if rr.Code != 400 {
			t.Fatalf("should not issue device access for %s: %d %s", target, rr.Code, rr.Body.String())
		}
	}
	rr := adminRequest(s, "POST", "claude/setup", map[string]any{"revision": s.snap.revision, "route": second.Name}, cookie, csrf)
	var out nativeSetupReply
	if rr.Code != 201 || json.Unmarshal(rr.Body.Bytes(), &out) != nil || out.URL != "http://localhost/second/" {
		t.Fatalf("setup did not target selected route: %d %s", rr.Code, rr.Body.String())
	}
	clients, err := s.store.callers()
	if err != nil || len(clients) != 1 || !reflect.DeepEqual(clients[0].Grants, []string{second.Name}) {
		t.Fatalf("unexpected grants for device: %+v, %v", clients, err)
	}
}

func TestClaudeSetupAvoidsRouteAndAccountCollisions(t *testing.T) {
	s := testService(t)
	claudeSetupAccount(t, s, "claude-code", "chatgpt")
	other := Route{Name: "claude-code", Account: "claude-code", LocalPrefix: "/claude-code/", Upstream: "https://chatgpt.com/backend-api/codex", Enabled: true}
	prefixCollision := Route{Name: "different-name", LocalPrefix: "/claude-code-2/", Upstream: "https://example.test", Enabled: true}
	configure(t, s, Config{Routes: []Route{other, prefixCollision}}, nil)
	before, err := s.store.account("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAdmin(t, s)
	out := claudeSetupPost(t, s, cookie, csrf)
	if len(s.snap.routes) != 3 || out.URL != "http://localhost/claude-code-3/" || !reflect.DeepEqual(s.snap.config.Routes[:2], []Route{otherWithDefaultAuth(other), otherWithDefaultAuth(prefixCollision)}) {
		t.Fatalf("collisions overwritten: %+v reply=%+v", s.snap.config.Routes, out)
	}
	after, err := s.store.account("claude-code")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("non-Claude account adopted: %+v %v", after, err)
	}
	created := s.snap.routes[2]
	next := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "connect", created.Name)
	if next.URL != out.URL || len(s.snap.routes) != 3 {
		t.Fatal("reconnect duplicated collision-safe route")
	}
}

func otherWithDefaultAuth(r Route) Route {
	r.CallerAuth = Auth{Header: "Authorization", Prefix: "Bearer "}
	return r
}

func TestClaudeSetupPreservesMoneySecretsAndBootstrapRefs(t *testing.T) {
	s := testService(t)
	claudeSetupAccount(t, s, "other-account", "chatgpt")
	secret := material{Origin: "https://example.test:443", Value: "synthetic-secret"}
	if _, err := s.store.db.Exec("INSERT INTO secrets VALUES(?,?,?)", "stored", secret.Origin, secret.Value); err != nil {
		t.Fatal(err)
	}
	refs := map[string]SecretRef{"stored": {Stored: "stored", Origin: secret.Origin}}
	bootstrap := map[string]AccountRef{"other-account": {Kind: "chatgpt", File: filepath.Join(t.TempDir(), "not-read")}}
	configure(t, s, Config{Routes: []Route{}, Secrets: refs, Accounts: bootstrap}, map[string]material{"stored": secret})
	money := MoneySettings{Timezone: "Australia/Sydney", Budget: &MoneyBudget{Currency: "AUD", LimitMicros: 12300000}, Rates: []MoneyRate{testMoneyRate()}}
	data, _ := json.Marshal(money)
	if _, err := s.store.db.Exec("UPDATE money_settings SET data=? WHERE id=1", string(data)); err != nil {
		t.Fatal(err)
	}
	before := claudeSetupDBState(t, s, "money_settings", "secrets", "accounts")
	cookie, csrf := loginAdmin(t, s)
	claudeSetupPost(t, s, cookie, csrf)
	for _, method := range []string{"POST", "DELETE"} {
		if method == "POST" {
			claudeSetupPost(t, s, cookie, csrf)
		} else {
			rr := adminRequest(s, method, "claude/setup", map[string]any{"revision": s.snap.revision}, cookie, csrf)
			if rr.Code != 200 {
				t.Fatal(rr.Code, rr.Body.String())
			}
		}
		c, revision, secrets, err := s.store.load()
		if err != nil || revision != s.snap.revision || !reflect.DeepEqual(c.Money, money) || !reflect.DeepEqual(c.Secrets, refs) || !reflect.DeepEqual(c.Accounts, bootstrap) || !reflect.DeepEqual(secrets, s.snap.secrets) {
			t.Fatalf("configuration lost canonical state: %+v %v", c, err)
		}
	}
	if got := claudeSetupDBState(t, s, "money_settings", "secrets", "accounts"); got != before {
		t.Fatalf("unrelated durable state changed\nbefore=%s\nafter=%s", before, got)
	}
}

func TestClaudeSetupURLUsesTrustedBrowserOrigin(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	s.opts.PublicURL = "https://proxy.example.test"
	body, _ := json.Marshal(map[string]any{"revision": s.snap.revision})
	r := httptest.NewRequest("POST", "http://internal.invalid/_proxy/api/claude/setup", strings.NewReader(string(body)))
	r.Host = "proxy.example.test"
	r.Header.Set("Origin", s.opts.PublicURL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("X-Forwarded-Host", "evil.test")
	r.Header.Set("X-Forwarded-Proto", "http")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out nativeSetupReply
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 201 || err != nil || out.URL != "https://proxy.example.test"+s.snap.routes[0].LocalPrefix {
		t.Fatalf("untrusted origin: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	// Setup must not initialize the native CA or touch client configuration.
	if s.claudeNative != nil {
		t.Fatal("setup initialized native transport")
	}
	if _, err := os.Stat(filepath.Join(s.opts.StateDir, "claude-native-ca.pem")); !os.IsNotExist(err) {
		t.Fatalf("setup created CA: %v", err)
	}
}
