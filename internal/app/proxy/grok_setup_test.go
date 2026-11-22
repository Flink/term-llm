package proxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/deviceflow"
)

func TestGrokSetupIsolationAndReconnect(t *testing.T) {
	s := testService(t)
	admin, csrf := loginAdmin(t, s)
	other, otherCSRF := loginAdmin(t, s)
	const path = "grok/setup"
	approval := make(chan deviceflow.Credentials, 1)
	s.deviceSetup.start = func(_ context.Context, kind string, _ http.RoundTripper) (deviceflow.Challenge, deviceflow.Poll, error) {
		if kind != "grok" {
			t.Fatalf("unexpected issuer %q", kind)
		}
		return deviceflow.Challenge{VerificationURL: "https://x.ai/device", UserCode: "TEST-GROK"}, func(ctx context.Context) (deviceflow.Credentials, error) {
			select {
			case creds := <-approval:
				return creds, nil
			case <-ctx.Done():
				return deviceflow.Credentials{}, ctx.Err()
			}
		}, nil
	}
	start := func(in deviceSetupInput) string {
		t.Helper()
		w := adminRequest(s, "POST", path, in, admin, csrf)
		challenge := readChatGPTSetupReply[deviceSetupChallenge](t, w, 201)
		if challenge.UserCode != "TEST-GROK" || challenge.VerificationURL != "https://x.ai/device" || challenge.ID == "" {
			t.Fatalf("unexpected challenge: %+v", challenge)
		}
		return "?id=" + challenge.ID
	}
	finish := func(id string, creds deviceflow.Credentials) deviceSetupStatus {
		t.Helper()
		approval <- creds
		s.deviceSetup.workers.Wait()
		return readChatGPTSetupReply[deviceSetupStatus](t, adminRequest(s, "GET", path+id, nil, admin, ""), 200)
	}
	if w := adminRequest(s, "POST", path, deviceSetupInput{}, admin, ""); w.Code != 403 {
		t.Fatalf("start without CSRF: %d", w.Code)
	}
	id := start(deviceSetupInput{})
	deviceSetupStatusTest(t, s, admin, path+id, deviceSetupStatus{Status: "pending"})
	for _, method := range []string{"GET", "DELETE"} {
		if w := adminRequest(s, method, chatGPTSetupPath+id, nil, admin, csrf); w.Code != 404 {
			t.Fatalf("cross-provider %s: %d", method, w.Code)
		}
		if w := adminRequest(s, method, path+id, nil, other, otherCSRF); w.Code != 404 {
			t.Fatalf("cross-session %s: %d", method, w.Code)
		}
	}
	if w := adminRequest(s, "DELETE", path+id, nil, admin, csrf); w.Code != 204 {
		t.Fatalf("cancel: %d", w.Code)
	}
	s.deviceSetup.workers.Wait()
	if _, err := s.store.account("grok"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("canceled attempt created account")
	}
	creds := chatGPTSetupTestCredentials()
	rev := s.snap.revision
	if got := finish(start(deviceSetupInput{}), creds); got != (deviceSetupStatus{Status: "connected", Route: "grok"}) {
		t.Fatalf("connect: %+v", got)
	}
	if s.snap.revision != rev+1 || len(s.snap.routes) != 1 || s.snap.routes[0].Profile != "grok" || s.snap.routes[0].origin != "https://cli-chat-proxy.grok.com:443" {
		t.Fatal("did not atomically provision the canonical Grok route")
	}
	issued := proxyResponse[issuedBrowserCaller](t, adminRequest(s, "POST", "callers", map[string]any{
		"name": "laptop", "providers": []string{"grok"}, "revision": s.snap.revision,
	}, admin, csrf), http.StatusCreated)
	req := httptest.NewRequest("GET", "/_proxy/client/v2/providers", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	discovery := proxyResponse[struct{ Providers []clientProviderInfo }](t, w, http.StatusOK)
	if len(discovery.Providers) != 1 || discovery.Providers[0].ID != "grok" || len(discovery.Providers[0].Routes) != 1 || discovery.Providers[0].Routes[0].Profile != "grok" {
		t.Fatalf("new Grok client received unexpected routes: %+v", discovery.Providers)
	}
	if w := adminRequest(s, "POST", chatGPTSetupPath, deviceSetupInput{Account: "grok"}, admin, csrf); w.Code != 409 {
		t.Fatalf("ChatGPT reconnect accepted Grok account: %d", w.Code)
	}
	prev := s.snap
	reconnect := deviceSetupInput{Account: "grok"}
	id = start(reconnect)
	if w := adminRequest(s, "DELETE", "accounts", accountInput{Name: "grok"}, admin, csrf); w.Code != 200 {
		t.Fatalf("signout: %d", w.Code)
	}
	if got := finish(id, creds); got.Status != "failed" {
		t.Fatalf("stale approval: %+v", got)
	}
	wrong := creds
	wrong.AccountID = "different-account"
	if got := finish(start(reconnect), wrong); got.Status != "failed" {
		t.Fatalf("different identity: %+v", got)
	}
	a, err := s.store.account("grok")
	if err != nil || a.State != "needs_login" || a.credentials.AccessToken != "" || a.identityHash == "" {
		t.Fatal("signed-out custody changed")
	}
	// File-managed reconnect renews custody without touching the mounted config or grants.
	s.opts.ConfigFile = "file-managed"
	if got := finish(start(reconnect), creds); got.Status != "connected" || s.snap != prev {
		t.Fatalf("original identity reconnect: %+v", got)
	}
	if _, err := s.store.authorize(issued.Token, s.snap.routes[0]); err != nil {
		t.Fatalf("file-managed reconnect lost the existing grant: %v", err)
	}
	// Custom OAuth authority must never receive production credentials, even if
	// the override appeared after the device challenge was returned.
	id = start(reconnect)
	a, err = s.store.account("grok")
	if err != nil {
		t.Fatal(err)
	}
	a.operator.Origins = []string{"https://custom.invalid:443"}
	if err := s.store.replaceAccount(context.Background(), a, a.Generation); err != nil {
		t.Fatal(err)
	}
	if got := finish(id, creds); got.Status != "failed" {
		t.Fatalf("late origin override: %+v", got)
	}
	if w := adminRequest(s, "POST", path, reconnect, admin, csrf); w.Code != 409 {
		t.Fatalf("custom-origin reconnect: %d", w.Code)
	}
}
