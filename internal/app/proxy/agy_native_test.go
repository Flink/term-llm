package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const agyTestAccess = "fixture-google-access"
const agyTestRefresh = "fixture-google-refresh"

func agyTestProxy(t *testing.T, handler http.Handler) (*Service, string, string) {
	t.Helper()
	s := testService(t)
	ca, err := agyProxyProvider.openCA(claudeForwardPrivateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewUnstartedServer(handler)
	up.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf}, MinVersion: tls.VersionTLS12}
	up.StartTLS()
	t.Cleanup(up.Close)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ca.certificatePEM))
	s.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	s.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if nativeProviderForAuthority(addr) != agyProxyProvider {
			return nil, fmt.Errorf("unexpected test upstream %q", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
	}
	a := newNativeAccount("agy", agyProxyProvider.kind)
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	configure(t, s, Config{Routes: []Route{{Name: "agy", LocalPrefix: "/agy/", Upstream: "https://" + agyAPIHost, Enabled: true, Account: a.Name, Usage: "auto", Billing: "plan"}}}, nil)
	operator, token, err := s.store.createCaller("Antigravity sign-in launcher", []string{"agy"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.accountWrite(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires) VALUES(?,?,?,?,?)`, a.Name, operator.ID, "agy", s.snap.routes[0].authority, time.Now().Add(time.Minute).Unix())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(s)
	t.Cleanup(proxy.Close)
	return s, proxy.URL, token
}

type agyTestBootstrap struct {
	claudeForwardBootstrapReply
	Access    string `json:"access_placeholder"`
	Refresh   string `json:"refresh_placeholder"`
	ExpiresIn int    `json:"expires_in"`
}

func agyTestDiscover(t *testing.T, root, token string, wantStatus int) agyTestBootstrap {
	t.Helper()
	req, _ := http.NewRequest("GET", root+"/agy/"+agyProxyProvider.discovery, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	status, body := agyTestDo(t, http.DefaultClient, req)
	var bootstrap agyTestBootstrap
	if status != wantStatus || status == 200 && (json.Unmarshal([]byte(body), &bootstrap) != nil || bootstrap.Route != "agy" || bootstrap.ProxyURL != root || strings.Contains(body, agyTestAccess) || strings.Contains(body, agyTestRefresh) || strings.Contains(body, "fixture-client-secret")) {
		t.Fatalf("unexpected bootstrap: %d %s", status, body)
	}
	return bootstrap
}

func agyTestHTTPClient(t *testing.T, root, token string, bootstrap agyTestBootstrap) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(bootstrap.CertificatePEM)) {
		t.Fatal("invalid bootstrap certificate")
	}
	proxy, _ := url.Parse(root)
	proxy.User = url.UserPassword("agy", token)
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func agyTestDo(t *testing.T, client *http.Client, r *http.Request) (int, string) {
	t.Helper()
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func agyTestOAuth(t *testing.T, client *http.Client, form url.Values) (int, string) {
	t.Helper()
	form.Set("client_id", agyProxyProvider.clientID)
	form.Set("client_secret", "fixture-client-secret")
	req, _ := http.NewRequest("POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return agyTestDo(t, client, req)
}

func TestAntigravityNativeProxyCustody(t *testing.T) {
	var exchanges, rotations atomic.Int32
	s, root, token := agyTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Anthropic-Beta") != "" {
			t.Errorf("unexpected upstream headers: %v", r.Header)
		}
		switch r.Host {
		case agyOAuthHost:
			if r.URL.Path != "/token" || r.Header.Get("Authorization") != "" || r.ParseForm() != nil {
				t.Error("invalid token request")
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				exchanges.Add(1)
				fmt.Fprint(w, `{"access_token":"`+agyTestAccess+`","refresh_token":"`+agyTestRefresh+`","expires_in":3600,"token_type":"Bearer","id_token":"must-not-escape"}`)
			case "refresh_token":
				rotations.Add(1)
				if r.Form.Get("refresh_token") != agyTestRefresh || r.Form.Get("client_secret") != "fixture-client-secret" {
					t.Error("Google received a refresh placeholder")
				}
				// Google normally omits refresh_token on refresh.
				fmt.Fprint(w, `{"access_token":"`+agyTestAccess+`","expires_in":3600,"token_type":"Bearer"}`)
			default:
				t.Error("unsupported grant reached upstream")
			}
		case agyProfileHost:
			if r.URL.Path != agyProfilePath || r.Header.Get("Authorization") != "Bearer "+agyTestAccess {
				t.Error("unexpected userinfo request")
			}
			fmt.Fprint(w, `{"id":"1234567890","email":"fixture@example.invalid","access_token":"must-not-escape"}`)
		case agyAPIHost:
			if r.URL.Path != "/v1internal:streamGenerateContent" || r.URL.RawQuery != "alt=sse" || r.Header.Get("Authorization") != "Bearer "+agyTestAccess {
				t.Error("unexpected Cloud Code request")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4}}}\n\n")
		default:
			t.Error("unexpected upstream host")
			http.Error(w, "unexpected host", 400)
		}
	}))
	worker1, worker1Token, err := s.store.createCaller("worker 1", []string{"agy"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	worker2Token := caller(t, s, "agy")
	operator := agyTestDiscover(t, root, token, http.StatusOK)
	if operator.Access != "" || operator.Refresh != "" {
		t.Fatal("operator discovery should go through native sign-in")
	}
	agyTestDiscover(t, root, worker1Token, http.StatusConflict)
	client := agyTestHTTPClient(t, root, token, operator)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"fixture"}, "code_verifier": {"fixture-verifier"}, "redirect_uri": {"https://antigravity.google/oauth-callback"}}
	beforeLogin := agyTestHTTPClient(t, root, worker1Token, operator)
	if status, _ := agyTestOAuth(t, beforeLogin, form); status != http.StatusForbidden || exchanges.Load() != 0 {
		t.Fatalf("worker could start operator sign-in: %d", status)
	}
	status, body := agyTestOAuth(t, client, form)
	var login claudeVaultReply
	if status != 200 || json.Unmarshal([]byte(body), &login) != nil || !strings.HasPrefix(login.AccessToken, agyProxyProvider.accessPrefix) ||
		strings.Contains(body, agyTestAccess) || strings.Contains(body, agyTestRefresh) || strings.Contains(body, "must-not-escape") {
		t.Fatalf("native login did not keep credentials in custody: %d %s", status, body)
	}
	worker1Bootstrap := agyTestDiscover(t, root, worker1Token, http.StatusOK)
	worker2Bootstrap := agyTestDiscover(t, root, worker2Token, http.StatusOK)
	if worker1Bootstrap.ExpiresIn != nativeAliasLifetime || worker1Bootstrap.Access == worker2Bootstrap.Access || worker1Bootstrap.Refresh == worker2Bootstrap.Refresh ||
		!strings.HasPrefix(worker1Bootstrap.Access, agyProxyProvider.accessPrefix) || !strings.HasPrefix(worker1Bootstrap.Refresh, agyProxyProvider.refreshPrefix) {
		t.Fatal("workers did not receive their own aliases")
	}
	client = agyTestHTTPClient(t, root, worker1Token, worker1Bootstrap)
	for _, step := range []struct {
		method, path, marker string
		code                 int
	}{
		{"GET", "https://www.googleapis.com" + agyProfilePath, "fixture@example.invalid", 200},
		{"GET", "https://www.googleapis.com" + agyProfilePath + "?alt=json", "unsupported", 403},
		{"POST", "https://" + agyAPIHost + "/v1internal:streamGenerateContent?alt=sse", "promptTokenCount", 200},
		{"POST", "https://" + agyAPIHost + "/v1internal:streamGenerateContent?alt=sse&key=ignore", "unsupported", 403},
		{"POST", "https://" + agyAPIHost + "/v1internal:arbitraryMethod", "unsupported", 403},
	} {
		req, _ := http.NewRequest(step.method, step.path, nil)
		req.Header.Set("Authorization", "Bearer "+worker1Bootstrap.Access)
		req.Header.Set("Cookie", "ignore=this")
		status, body := agyTestDo(t, client, req)
		if status != step.code || !strings.Contains(body, step.marker) || strings.Contains(body, "must-not-escape") {
			t.Fatalf("%s: %d %s", step.path, status, body)
		}
	}
	other := agyTestHTTPClient(t, root, worker2Token, worker2Bootstrap)
	req, _ := http.NewRequest("GET", "https://"+agyProfileHost+agyProfilePath, nil)
	req.Header.Set("Authorization", "Bearer "+worker1Bootstrap.Access)
	if status, _ := agyTestDo(t, other, req); status != 401 {
		t.Fatalf("different caller reused an OAuth placeholder: %d", status)
	}
	status, body = agyTestOAuth(t, client, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {worker1Bootstrap.Refresh}})
	var renewed claudeVaultReply
	if status != 200 || json.Unmarshal([]byte(body), &renewed) != nil || renewed.RefreshToken != worker1Bootstrap.Refresh || strings.Contains(body, agyTestRefresh) || rotations.Load() != 0 {
		t.Fatalf("refresh should reissue the alias without touching Google: %d %s", status, body)
	}
	if _, err := s.store.db.Exec(`UPDATE accounts SET expires=0 WHERE name='agy'`); err != nil {
		t.Fatal(err)
	}
	for _, worker := range []struct {
		client *http.Client
		access string
	}{{client, worker1Bootstrap.Access}, {other, worker2Bootstrap.Access}} {
		req, _ := http.NewRequest("POST", "https://"+agyAPIHost+"/v1internal:streamGenerateContent?alt=sse", nil)
		req.Header.Set("Authorization", "Bearer "+worker.access)
		if status, _ := agyTestDo(t, worker.client, req); status != 200 {
			t.Fatalf("worker request after shared refresh: %d", status)
		}
	}
	if exchanges.Load() != 1 || rotations.Load() != 1 {
		t.Fatalf("want one sign-in and one shared refresh, got %d and %d", exchanges.Load(), rotations.Load())
	}
	if err := s.store.revokeCaller(context.Background(), worker1.ID); err != nil {
		t.Fatal(err)
	}
	agyTestDiscover(t, root, worker1Token, http.StatusUnauthorized)
	agyTestDiscover(t, root, worker2Token, http.StatusOK)
}
