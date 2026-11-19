package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/oauth"
)

const chatGPTSetupPath = "chatgpt/setup"

func fakeChatGPTSetup(s *Service) chan *oauth.ChatGPTCredentials {
	result := make(chan *oauth.ChatGPTCredentials, 1)
	s.chatgptSetup.request = func(context.Context) (*oauth.ChatGPTDeviceCode, error) {
		return &oauth.ChatGPTDeviceCode{VerificationURL: oauth.ChatGPTDeviceVerificationURL, UserCode: "TEST-DEVICE"}, nil
	}
	s.chatgptSetup.wait = func(ctx context.Context, _ *oauth.ChatGPTDeviceCode) (*oauth.ChatGPTCredentials, error) {
		select {
		case v := <-result:
			return v, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return result
}

func chatGPTSetupTestCredentials() *oauth.ChatGPTCredentials {
	return &oauth.ChatGPTCredentials{AccessToken: "fixture-access-token", RefreshToken: "fixture-refresh-token", AccountID: "fixture-account-id", ExpiresAt: time.Now().Add(time.Hour).Unix()}
}

func readChatGPTSetupReply[T any](t *testing.T, w *httptest.ResponseRecorder, want int) T {
	t.Helper()
	var v T
	if w.Code != want || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("got HTTP %d %s, want %d and valid JSON", w.Code, w.Body, want)
	}
	for _, private := range []string{"fixture-access-token", "fixture-refresh-token", "fixture-account-id", "fixture-private-error"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("setup response contains private information")
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("setup response may be cached")
	}
	return v
}

func startChatGPTSetupTest(t *testing.T, s *Service, cookie *http.Cookie, csrf string, in chatGPTSetupInput) string {
	t.Helper()
	w := adminRequest(s, "POST", chatGPTSetupPath, in, cookie, csrf)
	v := readChatGPTSetupReply[chatGPTSetupChallenge](t, w, 201)
	if v.ID == "" || v.VerificationURL != oauth.ChatGPTDeviceVerificationURL || v.UserCode != "TEST-DEVICE" {
		t.Fatalf("invalid device challenge: %+v", v)
	}
	return chatGPTSetupPath + "?id=" + v.ID
}

func chatGPTSetupStatusTest(t *testing.T, s *Service, cookie *http.Cookie, path string, want chatGPTSetupStatus) {
	t.Helper()
	got := readChatGPTSetupReply[chatGPTSetupStatus](t, adminRequest(s, "GET", path, nil, cookie, ""), 200)
	if got != want {
		t.Fatalf("setup status %+v, want %+v", got, want)
	}
}

func TestChatGPTSetupProvisionsRouteAndAccountOnly(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	admin, csrf := loginAdmin(t, s)
	path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{})
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "pending"})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "connected", Route: "chatgpt"})
	if w := adminRequest(s, "DELETE", path, nil, admin, csrf); w.Code != 409 {
		t.Fatalf("cancel after commit: %d, want 409", w.Code)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	admin, _ = loginAdmin(t, restarted)
	if w := adminRequest(restarted, "GET", path, nil, admin, ""); w.Code != 404 {
		t.Fatalf("attempt status survived restart: %d", w.Code)
	}
	bearer := Auth{Header: "Authorization", Prefix: "Bearer "}
	want := []Route{
		{Name: "chatgpt", Provider: "chatgpt", LocalPrefix: "/chatgpt/", Account: "chatgpt", Enabled: true, Upstream: "https://chatgpt.com/backend-api/codex", CallerAuth: bearer, Usage: "auto", Billing: "plan", WebSocket: true},
		// Clients bind dictation, usage, embeddings and live by these fixed names.
		{Name: "chatgpt-backend", Provider: "chatgpt", LocalPrefix: "/chatgpt-backend/", Account: "chatgpt", Enabled: true, Upstream: "https://chatgpt.com/backend-api", CallerAuth: bearer, Billing: "plan"},
		{Name: "chatgpt-openai", Provider: "chatgpt", LocalPrefix: "/chatgpt-openai/", Account: "chatgpt", Enabled: true, Upstream: "https://api.openai.com/v1", CallerAuth: bearer, Billing: "unspecified", WebSocket: true},
	}
	if got := restarted.snap.config.Routes; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted route = %+v", got)
	}
	if a, err := restarted.store.account("chatgpt"); err != nil || a.Kind != "chatgpt" || a.State != "ready" || a.Generation != 1 || a.credentials.AccessToken != "fixture-access-token" {
		t.Fatal("account not durably activated with the route")
	}
	if callers, err := restarted.store.callers(); err != nil || len(callers) != 0 {
		t.Fatal("route setup must not issue a client or grant")
	}
}

// A companion name already in use is left alone; the sign-in still succeeds
// and the main route is created beside the operator's route.
func TestChatGPTSetupSkipsTakenCompanionRoutes(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	admin, csrf := loginAdmin(t, s)
	existing := Route{Name: "chatgpt-backend", LocalPrefix: "/chatgpt-backend/", Enabled: false, Upstream: "https://example.test/v1"}
	configure(t, s, Config{Routes: []Route{existing}}, nil)
	path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{Name: "work"})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "connected", Route: "work"})
	var names []string
	for _, r := range s.snap.config.Routes {
		names = append(names, r.Name+"@"+r.Account)
	}
	if want := []string{"chatgpt-backend@", "work@work", "chatgpt-openai@work"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("routes = %v, want %v", names, want)
	}
}

func TestChatGPTSetupCollisionAndRollback(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	admin, csrf := loginAdmin(t, s)
	manual := Route{Name: "manual", LocalPrefix: "/chatgpt/sub/", Enabled: false, Upstream: "https://example.test/v1"}
	configure(t, s, Config{Routes: []Route{manual}}, nil)
	for _, name := range []string{"manual", "chatgpt", "assets"} {
		w := adminRequest(s, "POST", chatGPTSetupPath, chatGPTSetupInput{Name: name}, admin, csrf)
		if w.Code != 409 {
			t.Fatalf("collision %q: %d %s", name, w.Code, w.Body)
		}
	}
	path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{Name: "alternate"})
	// Introduce a collision through the real config API after the user is sent
	// to OpenAI. Completion must recheck the live configuration.
	manual.LocalPrefix = "/alternate/"
	w := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{manual}}, admin, csrf)
	if w.Code != 200 {
		t.Fatalf("config update: %d %s", w.Code, w.Body)
	}
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "failed", Error: "This route prefix is reserved or overlaps an existing route; choose a different name"})

	previous := s.snap
	_, err := s.store.db.Exec(`CREATE TRIGGER refuse_chatgpt_config BEFORE UPDATE ON configuration BEGIN SELECT RAISE(FAIL,'fixture-private-error'); END`)
	if err != nil {
		t.Fatal(err)
	}
	path = startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{Name: "new"})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "failed", Error: "Could not save the ChatGPT route and account; existing configuration is unchanged"})
	if _, err := s.store.account("new"); !errors.Is(err, sql.ErrNoRows) || s.snap != previous {
		t.Fatal("failed config commit published or retained part of the setup")
	}
	_, persistedRevision, _, err := s.store.load()
	if err != nil || persistedRevision != previous.revision {
		t.Fatalf("failed setup changed the durable config revision: %d, %v", persistedRevision, err)
	}
}

func TestChatGPTSetupAuthorizationCancellationAndExpiry(t *testing.T) {
	for _, action := range []string{"cancel", "logout", "expire", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			s := testService(t)
			fakeChatGPTSetup(s)
			// Even if the issuer returns credentials alongside local cancellation,
			// the transaction must be fenced. No network requests occur.
			approval := make(chan struct{})
			s.chatgptSetup.wait = func(ctx context.Context, _ *oauth.ChatGPTDeviceCode) (*oauth.ChatGPTCredentials, error) {
				select {
				case <-approval:
				case <-ctx.Done():
				}
				return chatGPTSetupTestCredentials(), nil
			}
			admin, csrf := loginAdmin(t, s)
			outsider, outsiderCSRF := loginAdmin(t, s)
			req := func(method, path string, cookie *http.Cookie, token string, want int) {
				t.Helper()
				w := adminRequest(s, method, path, chatGPTSetupInput{}, cookie, token)
				if w.Code != want {
					t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, want, w.Body)
				}
			}
			req("POST", chatGPTSetupPath, nil, "", 401)
			req("POST", chatGPTSetupPath, admin, "", 403)
			path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{})
			req("POST", chatGPTSetupPath, outsider, outsiderCSRF, 409)
			req("GET", path, outsider, "", 404)
			req("DELETE", path, outsider, outsiderCSRF, 404)
			req("DELETE", path, admin, "", 403)
			switch action {
			case "cancel":
				req("DELETE", path, admin, csrf, 204)
				req("GET", path, admin, "", 404)
			case "logout":
				req("POST", "logout", admin, csrf, 200)
				req("GET", path, admin, "", 401)
			case "expire":
				s.chatgptSetup.mu.Lock()
				for _, a := range s.chatgptSetup.attempts {
					a.expires = time.Now().Add(-time.Second)
				}
				s.chatgptSetup.mu.Unlock()
				chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "failed", Error: chatGPTSetupExpired})
			case "shutdown":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			close(approval)
			s.chatgptSetup.workers.Wait()
			if action == "shutdown" {
				var err error
				s, err = Open(s.opts)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
			}
			if _, err := s.store.account("chatgpt"); !errors.Is(err, sql.ErrNoRows) || len(s.snap.routes) != 0 {
				t.Fatal("late OAuth completion mutated proxy after cancellation")
			}
		})
	}
}

func TestChatGPTSetupReconnectPreservesFileAndGrants(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	admin, csrf := loginAdmin(t, s)
	old := accountRecord{AccountInfo: AccountInfo{Name: "subscription", Kind: "chatgpt", Generation: 3, State: "ready"}, identityVersion: 2, credentials: accountCredentials{AccountID: "fixture-account-id"}}
	if err := s.store.replaceAccount(context.Background(), old, 0); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(s, "DELETE", "accounts", accountInput{Name: old.Name}, admin, csrf); w.Code != 200 {
		t.Fatalf("sign out: %d %s", w.Code, w.Body)
	}
	if a, err := s.store.account(old.Name); err != nil || a.credentials.AccountID != "" || a.chatGPTIdentityHash == "" {
		t.Fatal("sign out should retain only an identity digest for a later reconnect")
	}
	s.opts.ConfigFile = filepath.Join(t.TempDir(), "proxy.yaml")
	file := []byte("routes:\n- name: custom\n  local_prefix: /custom/\n  upstream: https://chatgpt.com/backend-api\n  account: subscription\n  allow: [GET /wham/usage]\n")
	if err := os.WriteFile(s.opts.ConfigFile, file, 0600); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(s, "POST", "reload", nil, admin, csrf); w.Code != 200 {
		t.Fatalf("reload: %d %s", w.Code, w.Body)
	}
	if w := adminRequest(s, "POST", chatGPTSetupPath, chatGPTSetupInput{}, admin, csrf); w.Code != 409 {
		t.Fatalf("provision in file mode: %d %s", w.Code, w.Body)
	}
	previous := s.snap
	token := caller(t, s, "custom")
	stream, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	s.sockets.Store("old-generation", &activeSocket{account: "subscription", cancel: cancel})
	defer s.sockets.Delete("old-generation")
	path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{Account: "subscription"})
	other := chatGPTSetupTestCredentials()
	other.AccountID = "different-workspace"
	result <- other
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "failed", Error: "Sign in to the original ChatGPT account and workspace, or create a separate route"})
	if a, err := s.store.account(old.Name); err != nil || a.State != "needs_login" || a.Generation != 4 || context.Cause(stream) != nil {
		t.Fatal("a different ChatGPT identity changed existing custody or closed streams")
	}
	path = startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{Account: "subscription"})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "connected", Route: "custom"})
	if s.snap != previous || context.Cause(stream) != errSocketRevoked {
		t.Fatal("reconnect altered the route snapshot or left a prior identity stream open")
	}
	if _, err := s.store.authorize(token, s.snap.routes[0]); err != nil {
		t.Fatal("reconnect lost the existing client grant")
	}
	if a, err := s.store.account(old.Name); err != nil || a.State != "ready" || a.Generation != 5 || a.identityVersion != 4 || a.credentials.RefreshToken != "fixture-refresh-token" {
		t.Fatal("reconnect did not replace account custody and advance its identity epoch")
	}
	if data, err := os.ReadFile(s.opts.ConfigFile); err != nil || string(data) != string(file) {
		t.Fatal("reconnect modified the mounted route file")
	}
}

func TestChatGPTSetupDeviceUnavailable(t *testing.T) {
	s := testService(t)
	fakeChatGPTSetup(s)
	s.chatgptSetup.request = func(context.Context) (*oauth.ChatGPTDeviceCode, error) {
		return nil, oauth.ErrChatGPTDeviceCodeNotEnabled
	}
	admin, csrf := loginAdmin(t, s)
	w := adminRequest(s, "POST", chatGPTSetupPath, chatGPTSetupInput{}, admin, csrf)
	got := readChatGPTSetupReply[map[string]string](t, w, 409)
	if !strings.Contains(got["error"], "device code settings") {
		t.Fatal("device-code settings hint missing")
	}
}

func TestChatGPTSetupPasskeyRevocationBeforeCommit(t *testing.T) {
	s, _ := passkeyFixture(t)
	result := fakeChatGPTSetup(s)
	initiator := newPasskeyBrowser(t, s)
	_, reauth := bootstrapPasskey(t, initiator)
	reauth("reauth", 1)
	w := initiator.send("POST", chatGPTSetupPath, chatGPTSetupInput{}, 201)
	id := readChatGPTSetupReply[chatGPTSetupChallenge](t, w, 201).ID
	initiator.send("POST", "logout", nil, 200)
	initiator.send("GET", chatGPTSetupPath+"?id="+id, nil, 401)
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	if _, err := s.store.account("chatgpt"); !errors.Is(err, sql.ErrNoRows) || len(s.snap.routes) != 0 {
		t.Fatal("a revoked passkey session committed a ChatGPT setup")
	}
}

func TestChatGPTSetupPasskeyAndSafeIssuerFailure(t *testing.T) {
	s, _ := passkeyFixture(t)
	fakeChatGPTSetup(s)
	s.chatgptSetup.wait = func(context.Context, *oauth.ChatGPTDeviceCode) (*oauth.ChatGPTCredentials, error) {
		return nil, errors.New("fixture-private-error")
	}
	b := newPasskeyBrowser(t, s)
	_, _ = bootstrapPasskey(t, b)
	w := b.send("POST", chatGPTSetupPath, chatGPTSetupInput{}, 201)
	id := readChatGPTSetupReply[chatGPTSetupChallenge](t, w, 201).ID
	s.chatgptSetup.workers.Wait()
	w = b.send("GET", chatGPTSetupPath+"?id="+id, nil, 200)
	status := readChatGPTSetupReply[chatGPTSetupStatus](t, w, 200)
	if status.Status != "failed" || status.Error == "" {
		t.Fatalf("issuer failure did not produce a safe error: %+v", status)
	}
}
