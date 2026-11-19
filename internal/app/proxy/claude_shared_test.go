package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	sharedFixtureAccess     = "synthetic-shared-issuer-access"
	sharedFixtureRefresh    = "synthetic-shared-issuer-refresh"
	sharedFixtureNextAccess = "synthetic-shared-issuer-next-access"
	sharedFixtureNextRotate = "synthetic-shared-issuer-next-refresh"
	sharedFixtureScope      = "user:profile user:inference"
	sharedFixtureExtra      = "synthetic-shared-issuer-extra-credential"
)

type claudeSharedHTTPFixture struct {
	service *Service
	server  *httptest.Server
	vendor  *httptest.Server
	roots   *x509.CertPool
	oauth   atomic.Int32
	mu      sync.Mutex
	access  []string
}

func newClaudeSharedHTTPFixture(t *testing.T, issuer, api http.HandlerFunc) *claudeSharedHTTPFixture {
	t.Helper()
	f := &claudeSharedHTTPFixture{roots: x509.NewCertPool()}
	ca, err := openClaudeForwardCA(claudeForwardPrivateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if !f.roots.AppendCertsFromPEM([]byte(ca.certificatePEM)) {
		t.Fatal("invalid synthetic vendor CA")
	}
	f.vendor = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.ServerName != r.Host || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			t.Error("unexpected TLS target or proxy credentials at synthetic vendor")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Host == claudeOAuthHost && r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/token":
			f.oauth.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("unexpected Authorization header at synthetic issuer")
			}
			issuer(w, r)
		case r.Host == claudeAPIHost && r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			f.mu.Lock()
			f.access = append(f.access, r.Header.Get("Authorization"))
			f.mu.Unlock()
			if api != nil {
				api(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"data":[]}`)
		case r.Host == claudeAPIHost && api != nil:
			api(w, r)
		default:
			t.Errorf("unexpected synthetic vendor request %s %s%s", r.Method, r.Host, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	f.vendor.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf}, MinVersion: tls.VersionTLS12}
	f.vendor.StartTLS()
	t.Cleanup(f.vendor.Close)
	f.startProxy(t, testService(t))
	return f
}

func (f *claudeSharedHTTPFixture) startProxy(t *testing.T, s *Service) {
	t.Helper()
	// There is no DNS/plugin/environment fallback: every upstream TLS socket
	// must go to the loopback fixture, or fail before dialing anything.
	s.transport.Proxy = nil
	s.transport.TLSClientConfig = &tls.Config{RootCAs: f.roots, MinVersion: tls.VersionTLS12}
	s.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != claudeAPIHost+":443" && addr != claudeOAuthHost+":443" {
			t.Errorf("unexpected upstream dial target %q", addr)
			return nil, errors.New("not a synthetic upstream")
		}
		return (&net.Dialer{}).DialContext(ctx, network, f.vendor.Listener.Addr().String())
	}
	f.service = s
	server := httptest.NewServer(s)
	server.Client().Timeout = 5 * time.Second
	server.Client().CheckRedirect = claudeSharedNoRedirect
	t.Cleanup(server.Close)
	f.server = server
}

func (f *claudeSharedHTTPFixture) assertAccesses(t *testing.T, want ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.access) != len(want) {
		t.Fatalf("synthetic API got %d requests, want %d", len(f.access), len(want))
	}
	for i, token := range want {
		if f.access[i] != "Bearer "+token {
			t.Errorf("synthetic API request %d did not use the expected issuer access token", i+1)
		}
	}
}

func claudeSharedNoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func claudeSharedSetup(t *testing.T, s *Service, cookie *http.Cookie, csrf, action, route string) nativeSetupReply {
	t.Helper()
	in := map[string]any{"revision": s.snap.revision}
	if action != "" {
		in["shared_action"] = action
	}
	if route != "" {
		in["route"] = route
	}
	rr := adminRequest(s, http.MethodPost, "claude/setup", in, cookie, csrf)
	if rr.Code != http.StatusCreated || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Claude setup (%q): HTTP %d (%s)", action, rr.Code, rr.Body.String())
	}
	var out nativeSetupReply
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL == "" || out.CallerID == "" || !strings.HasPrefix(out.Token, "sk-tlp-") || out.Revision != s.snap.revision {
		t.Fatalf("Claude setup (%q) returned incomplete connection metadata", action)
	}
	return out
}

type claudeSharedHTTPReply struct {
	status int
	header http.Header
	body   []byte
}

func claudeSharedRequest(client *http.Client, method, target, token, body string) (claudeSharedHTTPReply, error) {
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		return claudeSharedHTTPReply{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return claudeSharedHTTPReply{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return claudeSharedHTTPReply{status: resp.StatusCode, header: resp.Header, body: data}, err
}

type claudeSharedDiscovery struct {
	claudeForwardBootstrapReply
	ProtocolVersion   int    `json:"protocol_version"`
	TokenMode         string `json:"token_mode"`
	AccessPlaceholder string `json:"access_placeholder"`
	Scopes            string `json:"scopes"`
}

func (f *claudeSharedHTTPFixture) discover(t *testing.T, device nativeSetupReply, status int) claudeSharedDiscovery {
	t.Helper()
	u, err := url.Parse(device.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(u.Path, "/") {
		t.Fatal("Claude setup URL is not a route mount")
	}
	r, err := claudeSharedRequest(f.server.Client(), http.MethodGet, f.server.URL+u.EscapedPath()+claudeForwardDiscovery, device.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.status != status || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Claude discovery: HTTP %d, want %d and no-store (%s)", r.status, status, r.body)
	}
	claudeSharedAssertNoSecrets(t, "discovery", r.body, device.Token)
	var out claudeSharedDiscovery
	if status == http.StatusOK {
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatal(err)
		}
		if out.Route == "" || out.ProxyURL != f.server.URL || out.ProtocolVersion != 2 || out.TokenMode != "vault" || out.CertificatePEM == "" || stringsContainsPrivateKey(out.CertificatePEM) {
			t.Fatal("unexpected Claude v2 discovery metadata")
		}
	} else if bytes.Contains(r.body, []byte(`"access_placeholder"`)) {
		t.Fatal("unsuccessful discovery issued an access placeholder")
	}
	return out
}

var errClaudeSharedConnectDenied = errors.New("Claude CONNECT denied")

func claudeSharedNativeClient(t *testing.T, d claudeSharedDiscovery, token string) *http.Client {
	t.Helper()
	u, err := url.Parse(d.ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(d.Route, token)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(d.CertificatePEM)) {
		t.Fatal("invalid Claude discovery certificate")
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, resp *http.Response) error {
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired {
				return errClaudeSharedConnectDenied
			}
			return nil
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: claudeSharedNoRedirect}
}

func claudeSharedCode(t *testing.T, client *http.Client, code string, wantOK bool) {
	t.Helper()
	body := strings.Replace(claudeVaultCodePayload, "synthetic-code", code, 1)
	r, err := claudeSharedRequest(client, http.MethodPost, claudeTokenURL, "", body)
	if !wantOK && errors.Is(err, errClaudeSharedConnectDenied) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	claudeSharedAssertNoSecrets(t, "OAuth reply", r.body, code, "synthetic-verifier")
	if !wantOK {
		if r.status < 400 {
			t.Fatalf("disallowed exchange got HTTP %d", r.status)
		}
		return
	}
	if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("operator exchange got HTTP %d, want 200 and no-store (%s)", r.status, r.body)
	}
	var reply claudeVaultReply
	if err := json.Unmarshal(r.body, &reply); err != nil || reply.AccessToken == "" || reply.RefreshToken == "" || reply.Scope != sharedFixtureScope {
		t.Fatal("operator did not receive a usable, sanitized native OAuth reply")
	}
}

func claudeSharedOAuthRequest(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	var p map[string]string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("invalid OAuth JSON at synthetic issuer: %v", err)
		}
	} else {
		p = make(map[string]string)
		if err := r.ParseForm(); err != nil {
			t.Errorf("invalid OAuth form at synthetic issuer: %v", err)
		}
		for key := range r.PostForm {
			p[key] = r.PostForm.Get(key)
		}
	}
	if p["client_id"] != claudeClientID {
		t.Error("unexpected client ID at synthetic issuer")
	}
	return p
}

func claudeSharedIssueCode(w http.ResponseWriter, expires int, scope string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": sharedFixtureAccess, "refresh_token": sharedFixtureRefresh,
		"token_type": "Bearer", "expires_in": expires, "scope": scope, "id_token": sharedFixtureExtra,
	})
}

func claudeSharedAssertNoSecrets(t *testing.T, source string, data []byte, custom ...string) {
	t.Helper()
	secrets := append([]string{sharedFixtureAccess, sharedFixtureRefresh, sharedFixtureNextAccess, sharedFixtureNextRotate, sharedFixtureExtra}, custom...)
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatalf("plaintext credential in %s", source)
		}
	}
}

func claudeSharedAssertSealed(t *testing.T, s *Service, extra ...string) {
	t.Helper()
	for _, name := range []string{"proxy.db", "proxy.db-wal"} {
		data, err := os.ReadFile(filepath.Join(s.opts.StateDir, name))
		if name == "proxy.db-wal" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		claudeSharedAssertNoSecrets(t, name, data, extra...)
	}
}

func claudeSharedAPI(t *testing.T, client *http.Client, access string, wantOK bool) {
	t.Helper()
	r, err := claudeSharedRequest(client, http.MethodGet, "https://"+claudeAPIHost+"/v1/models", access, "")
	if !wantOK && errors.Is(err, errClaudeSharedConnectDenied) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if wantOK && (r.status != http.StatusOK || string(r.body) != `{"data":[]}`) {
		t.Fatalf("shared inference did not reach the synthetic vendor: HTTP %d (%s)", r.status, r.body)
	}
	if !wantOK && (r.status < 400 || r.status >= 500) {
		t.Fatalf("shared inference should have been denied, got HTTP %d", r.status)
	}
	claudeSharedAssertNoSecrets(t, "API reply", r.body, access)
}

func TestClaudeSharedBrowserSetupAndWorkerIsolation(t *testing.T) {
	f := newClaudeSharedHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		p := claudeSharedOAuthRequest(t, r)
		if p["grant_type"] != "authorization_code" || p["code_verifier"] != "synthetic-verifier" || p["redirect_uri"] != "http://localhost:1234/callback" {
			t.Error("unexpected subscription exchange at synthetic issuer")
		}
		switch p["code"] {
		case "console-code":
			claudeSharedIssueCode(w, 3600, sharedFixtureScope+" org:create_api_key")
		case "subscription-code":
			claudeSharedIssueCode(w, 3600, sharedFixtureScope+" unknown:scope")
		default:
			t.Error("an unauthorized code exchange reached the synthetic issuer")
			w.WriteHeader(http.StatusBadRequest)
		}
	}, nil)
	s := f.service
	cookie, csrf := loginAdmin(t, s)
	native := claudeSharedSetup(t, s, cookie, csrf, "", "")
	older := claudeSharedSetup(t, s, cookie, csrf, "create", "")
	another := claudeSharedSetup(t, s, cookie, csrf, "create", "")
	if native.URL == older.URL || native.URL == another.URL || older.URL == another.URL {
		t.Fatal("shared create reused an existing route")
	}
	initial := f.discover(t, older, http.StatusOK)
	operator := claudeSharedSetup(t, s, cookie, csrf, "connect", initial.Route)
	if operator.URL != older.URL || operator.Token == older.Token {
		t.Fatal("shared connect did not issue a new dedicated operator token on the existing route")
	}
	metadata := f.discover(t, operator, http.StatusOK)
	if metadata.AccessPlaceholder != "" || metadata.Scopes != "" {
		t.Fatal("operator discovery should be ordinary v2 native discovery")
	}
	// Guided setup waits for readiness. A pre-existing ordinary route grant
	// must still get a 409 from discovery and cannot spend an OAuth code.
	unready := nativeSetupReply{URL: operator.URL, Token: caller(t, s, metadata.Route)}
	f.discover(t, unready, http.StatusConflict)
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, unready.Token), "worker-before-login-code", false)
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, older.Token), "stale-operator-code", false)
	if f.oauth.Load() != 0 {
		t.Fatal("discovery, setup or unauthorized exchange contacted the issuer")
	}

	adminClient := claudeSharedNativeClient(t, metadata, operator.Token)
	claudeSharedCode(t, adminClient, "console-code", false)
	f.discover(t, unready, http.StatusConflict)
	// A failed native login can consume its one-time operator grant. Start a
	// fresh operator session without replacing the route.
	failedOperator := operator
	operator = claudeSharedSetup(t, s, cookie, csrf, "connect", metadata.Route)
	metadata = f.discover(t, operator, http.StatusOK)
	if metadata.AccessPlaceholder != "" || metadata.Scopes != "" {
		t.Fatal("fresh operator discovery should be ordinary v2 native discovery")
	}
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, operator.Token), "subscription-code", true)
	w1 := claudeSharedSetup(t, s, cookie, csrf, "", metadata.Route)
	w2 := claudeSharedSetup(t, s, cookie, csrf, "", metadata.Route)
	if w1.URL != operator.URL || w2.URL != operator.URL || w1.CallerID == w2.CallerID || w1.Token == w2.Token || w1.Token == operator.Token || w2.Token == operator.Token {
		t.Fatal("device setup did not issue independent connections on the selected route")
	}
	m1 := f.discover(t, w1, http.StatusOK)
	m2 := f.discover(t, w2, http.StatusOK)
	if m1.Route != metadata.Route || m2.Route != metadata.Route || m1.AccessPlaceholder == "" || m2.AccessPlaceholder == "" || m1.AccessPlaceholder == m2.AccessPlaceholder || m1.Scopes != sharedFixtureScope || m2.Scopes != sharedFixtureScope {
		t.Fatal("ready workers did not receive distinct placeholders with validated subscription scopes")
	}
	otherRoute := f.discover(t, another, http.StatusOK).Route
	otherWorker := nativeSetupReply{URL: another.URL, Token: caller(t, s, otherRoute)}
	f.discover(t, otherWorker, http.StatusConflict)

	client1, client2 := claudeSharedNativeClient(t, m1, w1.Token), claudeSharedNativeClient(t, m2, w2.Token)
	claudeSharedCode(t, client2, "worker-after-login-code", false)
	claudeSharedAPI(t, client1, m2.AccessPlaceholder, false)
	claudeSharedAPI(t, client2, m1.AccessPlaceholder, false)
	claudeSharedAPI(t, client1, w1.Token, false)
	claudeSharedAPI(t, client1, sharedFixtureAccess, false)
	claudeSharedAPI(t, client1, m1.AccessPlaceholder, true)
	claudeSharedAPI(t, client2, m2.AccessPlaceholder, true)
	f.assertAccesses(t, sharedFixtureAccess, sharedFixtureAccess)
	if f.oauth.Load() != 2 {
		t.Fatalf("synthetic issuer got %d exchanges, want the two operator requests only", f.oauth.Load())
	}

	w3 := claudeSharedSetup(t, s, cookie, csrf, "", metadata.Route)
	m3 := f.discover(t, w3, http.StatusOK)
	client3 := claudeSharedNativeClient(t, m3, w3.Token)
	for _, revoked := range []struct {
		method string
		device nativeSetupReply
		client *http.Client
		access string
	}{
		{http.MethodPut, w1, client1, m1.AccessPlaceholder},
		{http.MethodDelete, w2, client2, m2.AccessPlaceholder},
	} {
		in := map[string]any{"id": revoked.device.CallerID}
		if revoked.method == http.MethodPut {
			in["grants"] = []string{}
		}
		rr := adminRequest(s, revoked.method, "callers", in, cookie, csrf)
		if rr.Code != http.StatusOK {
			t.Fatalf("remove device access (%s): HTTP %d", revoked.method, rr.Code)
		}
		// A previously used client can try again on its old CONNECT pool.
		f.discover(t, revoked.device, http.StatusUnauthorized)
		claudeSharedAPI(t, revoked.client, revoked.access, false)
		if revoked.method == http.MethodPut {
			claudeSharedAPI(t, client2, m2.AccessPlaceholder, true)
		}
		claudeSharedAPI(t, client3, m3.AccessPlaceholder, true)
	}
	f.assertAccesses(t, slices.Repeat([]string{sharedFixtureAccess}, 5)...)
	claudeSharedAssertSealed(t, s, native.Token, older.Token, failedOperator.Token, operator.Token, another.Token, unready.Token, otherWorker.Token, w1.Token, w2.Token, w3.Token)
}

func claudeSharedEnrolledWorkers(t *testing.T, f *claudeSharedHTTPFixture) (nativeSetupReply, nativeSetupReply) {
	t.Helper()
	cookie, csrf := loginAdmin(t, f.service)
	op := claudeSharedSetup(t, f.service, cookie, csrf, "create", "")
	metadata := f.discover(t, op, http.StatusOK)
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, op.Token), "subscription-code", true)
	first := claudeSharedSetup(t, f.service, cookie, csrf, "", metadata.Route)
	second := claudeSharedSetup(t, f.service, cookie, csrf, "", metadata.Route)
	return first, second
}

func TestClaudeSharedRefreshOnceAcrossWorkers(t *testing.T) {
	bothRejected := make(chan struct{})
	var initialCalls atomic.Int32
	f := newClaudeSharedHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		p := claudeSharedOAuthRequest(t, r)
		switch p["grant_type"] {
		case "authorization_code":
			claudeSharedIssueCode(w, 3600, sharedFixtureScope+" unknown:scope")
		case "refresh_token":
			if p["refresh_token"] != sharedFixtureRefresh {
				t.Error("account refresh did not use the sealed issuer refresh token")
			}
			// The response omits scope and refresh_token. The same OAuth
			// validator must retain the previously validated values.
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"id_token":%q}`, sharedFixtureNextAccess, sharedFixtureExtra)
		default:
			t.Error("unexpected grant type at synthetic issuer")
			w.WriteHeader(http.StatusBadRequest)
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+sharedFixtureAccess {
			if initialCalls.Add(1) == 2 {
				close(bothRejected)
			}
			// Both workers must encounter the same rejected generation before
			// either response can trigger rotation. No sleep or expiry wait.
			select {
			case <-bothRejected:
			case <-r.Context().Done():
				return
			case <-time.After(4 * time.Second):
				t.Error("second worker never reached the rejected generation")
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"expired"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	w1, w2 := claudeSharedEnrolledWorkers(t, f)
	m1, m2 := f.discover(t, w1, http.StatusOK), f.discover(t, w2, http.StatusOK)
	c1, c2 := claudeSharedNativeClient(t, m1, w1.Token), claudeSharedNativeClient(t, m2, w2.Token)
	type result struct {
		reply claudeSharedHTTPReply
		err   error
	}
	results := make(chan result, 2)
	call := func(client *http.Client, access string) {
		r, err := claudeSharedRequest(client, http.MethodGet, "https://"+claudeAPIHost+"/v1/models", access, "")
		results <- result{r, err}
	}
	go call(c1, m1.AccessPlaceholder)
	go call(c2, m2.AccessPlaceholder)
	for range 2 {
		out := <-results
		if out.err != nil || out.reply.status != http.StatusUnauthorized || string(out.reply.body) != `{"error":"expired"}` {
			t.Errorf("rejected request was not preserved: status=%d err=%v", out.reply.status, out.err)
		}
	}
	if f.oauth.Load() != 2 {
		t.Fatalf("synthetic issuer saw %d grants, want one code exchange and one account refresh", f.oauth.Load())
	}
	f.assertAccesses(t, sharedFixtureAccess, sharedFixtureAccess)
	claudeSharedAPI(t, c1, m1.AccessPlaceholder, true)
	claudeSharedAPI(t, c2, m2.AccessPlaceholder, true)
	if m := f.discover(t, w2, http.StatusOK); m.Scopes != sharedFixtureScope {
		t.Fatal("refresh lost the previously validated scope set")
	}
	f.assertAccesses(t, sharedFixtureAccess, sharedFixtureAccess, sharedFixtureNextAccess, sharedFixtureNextAccess)
	if f.oauth.Load() != 2 {
		t.Fatal("worker requests refreshed a second time after the account rotation")
	}
	claudeSharedAssertSealed(t, f.service, w1.Token, w2.Token)
}

func TestClaudeSharedRefreshFenceSurvivesRestart(t *testing.T) {
	var f *claudeSharedHTTPFixture
	f = newClaudeSharedHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		p := claudeSharedOAuthRequest(t, r)
		switch p["grant_type"] {
		case "authorization_code":
			// In the proactive refresh window without sleeping for expiration.
			claudeSharedIssueCode(w, 1, sharedFixtureScope)
		case "refresh_token":
			if p["refresh_token"] != sharedFixtureRefresh {
				t.Error("wrong credential at synthetic issuer")
			}
			// Read through SQLite while the issuer is still handling the POST.
			// An uncommitted or after-send fence cannot satisfy this assertion.
			var count int
			err := f.service.store.db.QueryRowContext(r.Context(), `SELECT count(*) FROM accounts WHERE state='refreshing'`).Scan(&count)
			if err != nil || count != 1 {
				t.Errorf("refresh send preceded the durable fence: count=%d err=%v", count, err)
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":3600,"scope":%q}`, sharedFixtureNextAccess, sharedFixtureNextRotate, sharedFixtureScope)
		default:
			t.Error("unexpected grant type at synthetic issuer")
			w.WriteHeader(http.StatusBadRequest)
		}
	}, nil)
	w1, w2 := claudeSharedEnrolledWorkers(t, f)
	m1, m2 := f.discover(t, w1, http.StatusOK), f.discover(t, w2, http.StatusOK)
	before := f.service
	// Simulate loss of the terminal rotation write after the issuer consumed
	// the old token. Leave the before-send fence alone.
	if _, err := before.store.db.Exec(`CREATE TRIGGER shared_test_reject_refresh_result BEFORE UPDATE ON accounts
		WHEN OLD.state='refreshing' AND NEW.state='ready' BEGIN SELECT RAISE(ABORT, 'synthetic result write failure'); END`); err != nil {
		t.Fatal(err)
	}
	c1 := claudeSharedNativeClient(t, m1, w1.Token)
	failed, err := claudeSharedRequest(c1, http.MethodGet, "https://"+claudeAPIHost+"/v1/models", m1.AccessPlaceholder, "")
	if err != nil || failed.status < 400 || f.oauth.Load() != 2 {
		t.Fatalf("rotation did not fail after exactly one issuer refresh: HTTP %d, grants=%d, err=%v", failed.status, f.oauth.Load(), err)
	}
	claudeSharedAssertNoSecrets(t, "failed rotation", failed.body)
	claudeSharedAssertSealed(t, before, w1.Token, w2.Token)
	f.server.Close()
	if err = before.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(before.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	f.startProxy(t, reopened)
	// Reuse the second worker's pre-restart access and trust material. A
	// freshly discovered worker must ask the operator to connect again.
	m2.ProxyURL = f.server.URL
	c2 := claudeSharedNativeClient(t, m2, w2.Token)
	retry, err := claudeSharedRequest(c2, http.MethodGet, "https://"+claudeAPIHost+"/v1/models", m2.AccessPlaceholder, "")
	if err != nil && !errors.Is(err, errClaudeSharedConnectDenied) || err == nil && retry.status < 400 {
		t.Fatalf("interrupted account unexpectedly usable after restart: HTTP %d, err=%v", retry.status, err)
	}
	f.discover(t, w2, http.StatusConflict)
	if f.oauth.Load() != 2 {
		t.Fatal("restarted proxy resent a consumed refresh credential")
	}
	f.assertAccesses(t)
	claudeSharedAssertSealed(t, reopened, w1.Token, w2.Token)
}
