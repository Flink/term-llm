package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

type claudeForwardFixture struct {
	service     *Service
	server      *httptest.Server
	token       string
	access      string
	refresh     string
	roots       *x509.CertPool
	vendorCalls atomic.Int32
	oauthCalls  atomic.Int32
	probeCalls  atomic.Int32
	probe       func(http.ResponseWriter, *http.Request)
	oauth       func(http.ResponseWriter, *http.Request)
	api         func(http.ResponseWriter, *http.Request)
}

func newClaudeForwardFixture(t *testing.T) *claudeForwardFixture {
	t.Helper()
	return newClaudeForwardFixtureWithConcurrency(t, 1)
}

func newClaudeForwardFixtureWithConcurrency(t *testing.T, concurrency int) *claudeForwardFixture {
	t.Helper()
	return newClaudeForwardFixtureWithPolicy(t, concurrency, []string{"GET /api/oauth/usage", "GET /v1/models", "POST /v1/messages"})
}

func newClaudeForwardFixtureWithPolicy(t *testing.T, concurrency int, allow []string) *claudeForwardFixture {
	t.Helper()
	f := &claudeForwardFixture{service: testService(t)}
	f.service.opts.MaxConcurrent = concurrency
	f.service.slots = make(chan struct{}, concurrency)
	vendorCA, err := openClaudeForwardCA(claudeForwardPrivateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Public startup probes are distinct from authenticated API traffic and
		// OAuth POSTs. Keep their matching independent of the production helper.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.RawQuery == "" && !r.URL.ForceQuery &&
			(r.Host == claudeAPIHost && r.URL.EscapedPath() == "/api/hello" || r.Host == claudeOAuthHost && r.URL.EscapedPath() == "/v1/oauth/hello") {
			f.probeCalls.Add(1)
			if r.TLS.ServerName != r.Host || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 {
				t.Errorf("public probe target or framing changed: %s %s", r.Host, r.URL)
			}
			for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Scoped-Key", "X-API-Key"} {
				if len(r.Header.Values(header)) != 0 {
					t.Errorf("public probe leaked %s", header)
				}
			}
			if r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("public probe negotiation changed: %v", r.Header)
			}
			if f.probe != nil {
				f.probe(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"hello":"fixture"}`)
			return
		}
		if r.Host == claudeOAuthHost {
			f.oauthCalls.Add(1)
			if r.ContentLength <= 0 || len(r.TransferEncoding) != 0 {
				t.Error("OAuth issuer request must use Content-Length, not chunked framing")
			}
			if r.TLS.ServerName != claudeOAuthHost || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Scoped-Key") != "" {
				t.Error("OAuth credentials or target changed")
			}
			if f.oauth != nil {
				f.oauth(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference","nested":{"access_token":"must-not-escape"},"id_token":"must-not-escape"}`)
			return
		}
		f.vendorCalls.Add(1)
		if r.Host != claudeAPIHost || r.TLS.ServerName != claudeAPIHost {
			t.Errorf("vendor target changed: %s %s", r.Host, r.TLS.ServerName)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-vendor-access" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Scoped-Key") != "" || r.Header.Get("X-API-Key") != "" {
			t.Errorf("worker credentials leaked or custody bypassed: %v", r.Header)
		}
		if !headerHasToken(r.Header, "Anthropic-Beta", "oauth-2025-04-20") {
			t.Error("missing account OAuth beta")
		}
		if f.api != nil {
			f.api(w, r)
			return
		}
		if r.URL.Path == "/api/oauth/usage" && (r.Method != "GET" || r.URL.RawQuery != "fixture=a%2Fb") {
			t.Errorf("usage request changed: %s %s", r.Method, r.URL)
		}
		if r.URL.Path == "/v1/messages" {
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"model":"fixture"}` {
				t.Errorf("body changed %q %v", body, err)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_fixture","type":"message","model":"fixture","usage":{"input_tokens":7,"output_tokens":3},"content":[]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":"fixture"}`)
	}))
	up.TLS = &tls.Config{Certificates: []tls.Certificate{vendorCA.leaf}, MinVersion: tls.VersionTLS12}
	up.StartTLS()
	t.Cleanup(up.Close)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(vendorCA.certificatePEM))
	f.service.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	f.service.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !claudeForwardAuthority(addr) {
			return nil, fmt.Errorf("unexpected vendor target")
		}
		return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
	}
	a, key := seedReadyClaudeShared(t, f.service, "claude", "synthetic-vendor-access", "synthetic-vendor-refresh")
	configure(t, f.service, Config{Routes: []Route{{Name: "claude", LocalPrefix: "/claude/", Upstream: "https://" + claudeAPIHost, Enabled: true, Account: "claude", Usage: "auto", Billing: "plan", CallerAuth: Auth{Header: "X-Scoped-Key", Prefix: "Local "}, Allow: allow}}}, nil)
	f.token = caller(t, f.service, "claude")
	scope := claudeSharedTestCallerScope(t, f.service, f.service.snap.routes[0], f.token)
	f.access = claudeSharedAlias(key, a, scope, claudeProxyProvider.accessPrefix)
	f.refresh = claudeSharedAlias(key, a, scope, claudeProxyProvider.refreshPrefix)

	// Exercise the integration API without requiring the owning parent to edit
	// server.go while this isolated backend is under construction.
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.service.claudeForward(w, r) {
			f.service.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	bootstrap := f.bootstrap(t, f.token)
	if bootstrap.AccessPlaceholder != f.access {
		t.Fatal("bootstrap did not emit the caller-scoped access alias")
	}
	f.roots = x509.NewCertPool()
	if !f.roots.AppendCertsFromPEM([]byte(bootstrap.CertificatePEM)) {
		t.Fatal("invalid bootstrap certificate")
	}
	return f
}

type claudeForwardBootstrapReply struct {
	AccessPlaceholder string `json:"access_placeholder"`
	CertificatePEM    string `json:"certificate_pem"`
	ProxyURL          string `json:"proxy_url"`
	Route             string `json:"route"`
}

func (f *claudeForwardFixture) bootstrap(t *testing.T, token string) claudeForwardBootstrapReply {
	t.Helper()
	req, err := http.NewRequest("GET", f.server.URL+"/claude/"+claudeForwardDiscovery, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Scoped-Key", "Local "+token)
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("bootstrap status %d: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("bootstrap is cacheable")
	}
	var result claudeForwardBootstrapReply
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ProxyURL != f.server.URL || result.Route != "claude" || stringsContainsPrivateKey(result.CertificatePEM) {
		t.Fatalf("unsafe bootstrap response %+v", result)
	}
	return result
}

type claudeForwardTestReaderConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *claudeForwardTestReaderConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (f *claudeForwardFixture) connect(t *testing.T, target, auth string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	u, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, auth); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
	}
	return conn, reader, resp.StatusCode
}
func claudeForwardBasic(route, token string) string {
	return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(route+":"+token)) + "\r\n"
}
func (f *claudeForwardFixture) native(t *testing.T) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	return f.nativeHost(t, claudeAPIHost, f.token)
}
func (f *claudeForwardFixture) nativeHost(t *testing.T, host, token string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	return f.nativeRouteHost(t, "claude", host, token)
}

func (f *claudeForwardFixture) nativeRouteHost(t *testing.T, route, host, token string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	raw, reader, status := f.connect(t, host+":443", claudeForwardBasic(route, token))
	if status != 200 {
		t.Fatalf("CONNECT status %d", status)
	}
	conn := tls.Client(&claudeForwardTestReaderConn{Conn: raw, reader: reader}, &tls.Config{RootCAs: f.roots, ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if conn.ConnectionState().NegotiatedProtocol != "http/1.1" {
		t.Fatal("native tunnel is not HTTP/1.1")
	}
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewReader(conn)
}
func claudeForwardNativeCall(t *testing.T, conn net.Conn, reader *bufio.Reader, method, path, host, token string, extra string) (int, string) {
	t.Helper()
	body := ""
	if method == "POST" {
		body = `{"model":"fixture"}`
	}
	_, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n%s\r\n%s", method, path, host, token, len(body), extra, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func TestClaudeForwardUsageInferenceAndOrdinaryPolicy(t *testing.T) {
	f := newClaudeForwardFixture(t)
	if f.vendorCalls.Load() != 0 {
		t.Fatal("bootstrap performed vendor I/O")
	}
	records, _, err := f.service.store.records(nil, 100)
	if err != nil || len(records) != 0 {
		t.Fatal("bootstrap created inference evidence", records, err)
	}
	conn, reader := f.native(t)
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/api/oauth/usage?fixture=a%2Fb", claudeAPIHost, f.access, "X-API-Key: worker-placeholder\r\n"); code != 200 {
		t.Fatal("usage", code)
	}
	if code, _ := claudeForwardNativeCall(t, conn, reader, "POST", "/v1/messages", claudeAPIHost+":443", f.access, ""); code != 200 {
		t.Fatal("inference", code)
	}
	for _, path := range []string{"/_proxy/api/state", "/v1/forbidden", "/api/oauth/profile", "/v1/mcp_servers"} {
		if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", path, claudeAPIHost, f.access, ""); code != 403 {
			t.Fatal("ordinary policy bypass", path, code)
		}
	}
	if f.vendorCalls.Load() != 2 {
		t.Fatal("denied request dispatched", f.vendorCalls.Load())
	}
	records, _, err = f.service.store.records(nil, 100)
	if err != nil || len(records) != 3 {
		t.Fatal("ordinary ledger not used", len(records), err)
	}
	found := false
	for _, rec := range records {
		b, _ := json.Marshal(rec)
		if strings.Contains(string(b), f.token) || strings.Contains(string(b), "synthetic-vendor-access") {
			t.Fatal("credential in ledger")
		}
		if rec.Path == "/v1/messages" {
			found = true
			if rec.Usage == nil || rec.Usage.Usage.Input != 7 || rec.Usage.Usage.Output != 3 {
				t.Fatalf("missing observed usage %+v", rec.Usage)
			}
		}
	}
	if !found {
		t.Fatal("inference not recorded")
	}
}

func TestClaudeForwardEnforcesSharedSurfaceEvenWithoutRouteAllowlist(t *testing.T) {
	f := newClaudeForwardFixtureWithPolicy(t, 1, nil)
	f.api = func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || (r.Method == "POST" && string(body) != `{"model":"fixture"}`) {
			t.Errorf("request body not forwarded intact: %q, %v", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "%s %s", r.Method, r.URL.RequestURI())
	}
	conn, reader := f.native(t)
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/oauth/usage?fixture=a%2Fb"},
		{"GET", "/api/claude_code/settings"},
		{"POST", "/v1/messages"},
	} {
		code, body := claudeForwardNativeCall(t, conn, reader, endpoint.method, endpoint.path, claudeAPIHost, f.access, "")
		if code != http.StatusAccepted || body != endpoint.method+" "+endpoint.path {
			t.Fatalf("%s %s = %d %q", endpoint.method, endpoint.path, code, body)
		}
	}
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/v1/mcp_servers?cursor=a%2Fb"},
		{"POST", "/api/event_logging/v2/batch"},
		{"POST", "/api/artifacts/mcp"},
		{"GET", "/api/future-endpoint"},
	} {
		if code, _ := claudeForwardNativeCall(t, conn, reader, endpoint.method, endpoint.path, claudeAPIHost, f.access, ""); code != http.StatusForbidden {
			t.Fatalf("unsupported shared endpoint %s %s: %d", endpoint.method, endpoint.path, code)
		}
	}
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, "invalid-placeholder", ""); code != http.StatusUnauthorized {
		t.Fatalf("invalid native token = %d, want 401", code)
	}
	if out := call(f.service, "GET", "/claude/v1/models", "X-Scoped-Key", f.token, nil); out.Code != http.StatusBadGateway {
		t.Fatalf("native account used without a native session: %d", out.Code)
	}
	if f.vendorCalls.Load() != 3 {
		t.Fatalf("upstream calls = %d, want 3 authenticated native requests", f.vendorCalls.Load())
	}
}

func TestClaudeForwardCONNECTAuthenticationAndTargets(t *testing.T) {
	f := newClaudeForwardFixture(t)
	tests := []struct {
		name, target, auth string
		want               int
	}{
		{"no auth", claudeAPIHost + ":443", "", 407},
		{"wrong token", claudeAPIHost + ":443", claudeForwardBasic("claude", "wrong"), 407},
		{"admin token", claudeAPIHost + ":443", claudeForwardBasic("claude", strings.Repeat("a", 64)), 407},
		{"wrong route", claudeAPIHost + ":443", claudeForwardBasic("other", f.token), 407},
		{"duplicate auth", claudeAPIHost + ":443", claudeForwardBasic("claude", f.token) + claudeForwardBasic("claude", f.token), 407},
		{"other host", "evil.example:443", claudeForwardBasic("claude", f.token), 400},
		{"other port", claudeAPIHost + ":8443", claudeForwardBasic("claude", f.token), 400},
		{"implicit port", claudeAPIHost, claudeForwardBasic("claude", f.token), 400},
		{"trailing dot", claudeAPIHost + ".:443", claudeForwardBasic("claude", f.token), 400},
		{"localhost", "127.0.0.1:443", claudeForwardBasic("claude", f.token), 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, code := f.connect(t, tt.target, tt.auth)
			if code != tt.want {
				t.Fatalf("got %d want %d", code, tt.want)
			}
		})
	}
	if f.vendorCalls.Load() != 0 {
		t.Fatal("invalid tunnel reached vendor")
	}
}

func TestClaudeForwardTLSRejectsWrongTrustSNIAndProtocol(t *testing.T) {
	// These cases test protocol validation, not admission. Client Close does not
	// acknowledge server-side slot release; allow all sequential rejection cases
	// to drain independently. Admission and MaxConcurrent=1 have separate tests.
	f := newClaudeForwardFixtureWithConcurrency(t, 16)
	for _, tt := range []struct {
		name, sni string
		roots     *x509.CertPool
		version   uint16
		alpn      []string
	}{
		{"wrong SNI", "evil.example", f.roots, tls.VersionTLS12, []string{"http/1.1"}},
		{"other official host SNI", claudeOAuthHost, f.roots, tls.VersionTLS12, []string{"http/1.1"}},
		{"missing SNI", "", f.roots, tls.VersionTLS12, []string{"http/1.1"}},
		{"untrusted CA", claudeAPIHost, x509.NewCertPool(), tls.VersionTLS12, []string{"http/1.1"}},
		{"TLS1.1", claudeAPIHost, f.roots, tls.VersionTLS11, []string{"http/1.1"}},
		{"HTTP2", claudeAPIHost, f.roots, tls.VersionTLS12, []string{"h2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, reader, status := f.connect(t, claudeAPIHost+":443", claudeForwardBasic("claude", f.token))
			if status != 200 {
				t.Fatal(status)
			}
			config := &tls.Config{ServerName: tt.sni, RootCAs: tt.roots, MinVersion: tls.VersionTLS10, MaxVersion: tt.version, NextProtos: tt.alpn}
			// Skip client-side hostname validation only for the missing-SNI test; the
			// server must still reject it in GetConfigForClient.
			if tt.sni == "" {
				config.InsecureSkipVerify = true
			}
			conn := tls.Client(&claudeForwardTestReaderConn{Conn: raw, reader: reader}, config)
			if err := conn.Handshake(); err == nil {
				t.Fatal("accepted invalid TLS client")
			}
			conn.Close()
		})
	}
}

func TestClaudeForwardNativeRejectsHostAuthAndTraversal(t *testing.T) {
	// These cases test protocol validation, not admission. Client Close does not
	// acknowledge server-side slot release; allow all sequential rejection cases
	// to drain independently. Admission and MaxConcurrent=1 have separate tests.
	f := newClaudeForwardFixtureWithConcurrency(t, 16)
	for _, tt := range []struct {
		name, method, path, host, token, extra string
		want                                   int
	}{
		{"wrong host", "GET", "/v1/models", "evil.example", f.access, "", 400},
		{"other official Host", "GET", "/v1/models", claudeOAuthHost, f.access, "", 400},
		{"wrong port", "GET", "/v1/models", claudeAPIHost + ":8443", f.access, "", 400},
		{"absolute URL", "GET", "https://" + claudeAPIHost + "/v1/models", claudeAPIHost, f.access, "", 400},
		{"network path", "GET", "//evil.example/v1/models", claudeAPIHost, f.access, "", 400},
		{"dotdot", "GET", "/v1/../_proxy/state", claudeAPIHost, f.access, "", 400},
		{"escaped traversal", "GET", "/v1/%252e%252e/_proxy/state", claudeAPIHost, f.access, "", 400},
		{"backslash", "GET", "/v1/%5csecret", claudeAPIHost, f.access, "", 400},
		{"nested CONNECT", "CONNECT", claudeAPIHost + ":443", claudeAPIHost, f.access, "", 400},
		{"wrong bearer", "GET", "/v1/models", claudeAPIHost, "different-scoped-token", "", 401},
		{"duplicate bearer", "GET", "/v1/models", claudeAPIHost, f.access, "Authorization: Bearer " + f.access + "\r\n", 401},
		{"proxy credential inside TLS", "GET", "/v1/models", claudeAPIHost, f.access, claudeForwardBasic("claude", f.access), 401},
		{"upgrade", "GET", "/v1/models", claudeAPIHost, f.access, "Connection: Upgrade\r\nUpgrade: websocket\r\n", 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, reader := f.native(t)
			defer conn.Close()
			code, _ := claudeForwardNativeCall(t, conn, reader, tt.method, tt.path, tt.host, tt.token, tt.extra)
			if code != tt.want {
				t.Fatalf("got %d want %d", code, tt.want)
			}
		})
	}
	if f.vendorCalls.Load() != 0 {
		t.Fatal("invalid native request reached vendor")
	}
}
