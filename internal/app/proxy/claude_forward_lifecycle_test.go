package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeForwardWaitSockets(t *testing.T, s *Service) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.socketWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("native tunnel did not release lifecycle fence")
	}
}

func TestClaudeForwardIndependentTunnelAdmission(t *testing.T) {
	f := newClaudeForwardFixture(t)
	conn, reader := f.native(t)
	_, _, status := f.connect(t, claudeAPIHost+":443", claudeForwardBasic("claude", f.token))
	if status != 503 {
		t.Fatal("unbounded native admission", status)
	}
	// The held native slot is not a held request slot: MaxConcurrent=1 still
	// permits ordinary forwarding, discovery and subsequent tunnel requests.
	f.bootstrap(t, f.token)
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, f.access, ""); code != 200 {
		t.Fatal("request slot deadlocked", code)
	}
	conn.Close()
	claudeForwardWaitSockets(t, f.service)
	next, _ := f.native(t)
	next.Close()
	claudeForwardWaitSockets(t, f.service)
}

func TestClaudeForwardRevocationAndShutdownCloseTunnels(t *testing.T) {
	for _, kind := range []string{"grant", "caller", "account", "route", "shutdown", "pending-handshake"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeForwardFixture(t)
			var conn net.Conn
			if kind == "pending-handshake" {
				var status int
				conn, _, status = f.connect(t, claudeAPIHost+":443", claudeForwardBasic("claude", f.token))
				if status != 200 {
					t.Fatal(status)
				}
			} else {
				conn, _ = f.native(t)
			}
			if kind == "shutdown" || kind == "pending-handshake" {
				if err := f.service.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				f.service.mu.Lock()
				ar, caller, err := f.service.claudeForwardRouteLocked("claude", f.token)
				if err != nil {
					f.service.mu.Unlock()
					t.Fatal(err)
				}
				switch kind {
				case "grant":
					err = f.service.store.updateGrants(caller.ID, nil, f.service.snap)
					f.service.cancelCallerSockets(caller.ID)
				case "caller":
					_, err = f.service.store.db.Exec("UPDATE callers SET revoked=1 WHERE id=?", caller.ID)
					f.service.cancelCallerSockets(caller.ID)
				case "account":
					f.service.cancelAccountSockets(ar.Account)
				case "route":
					f.service.reconcileSockets(&snapshot{})
				}
				f.service.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				claudeForwardWaitSockets(t, f.service)
			}
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var one [1]byte
			if _, err := conn.Read(one[:]); err == nil {
				t.Fatal("revoked tunnel remains open")
			}
		})
	}
}

func TestClaudeForwardReauthorizesEachNativeRequest(t *testing.T) {
	f := newClaudeForwardFixture(t)
	conn, reader := f.native(t)
	// Even absent the proactive admin cancellation hook, a database grant change
	// is authoritative for every request on an already-established TLS session.
	f.service.mu.Lock()
	_, caller, err := f.service.claudeForwardRouteLocked("claude", f.token)
	if err == nil {
		err = f.service.store.updateGrants(caller.ID, nil, f.service.snap)
	}
	f.service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, f.access, ""); code != 401 {
		t.Fatal("stale tunnel grant", code)
	}
	if f.vendorCalls.Load() != 0 {
		t.Fatal("revoked request dispatched")
	}
}

func TestClaudeForwardFollowsCurrentLocalPrefixAndAuth(t *testing.T) {
	f := newClaudeForwardFixture(t)
	conn, reader := f.native(t)
	f.service.mu.Lock()
	r := f.service.snap.routes[0].Route
	r.LocalPrefix = "/moved-claude/"
	r.CallerAuth = Auth{Header: "Authorization", Prefix: "Bearer "}
	snap, err := compile(Config{Routes: []Route{r}}, f.service.snap.revision+1, nil)
	if err == nil {
		err = f.service.store.saveConfig(snap, false)
	}
	if err == nil {
		f.service.snap = snap
	}
	f.service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := claudeForwardNativeCall(t, conn, reader, "GET", "/v1/models", claudeAPIHost, f.access, ""); code != 200 {
		t.Fatal("stale local mount or auth", code)
	}
}

func TestClaudeForwardBootstrapSecurityAndEligibility(t *testing.T) {
	f := newClaudeForwardFixture(t)
	unscoped := caller(t, f.service)
	tests := []struct {
		name, method, host, path, token string
		want                            int
	}{
		{"no token", "GET", "localhost", "/claude/" + claudeForwardDiscovery, "", 401},
		{"wrong scope", "GET", "localhost", "/claude/" + claudeForwardDiscovery, unscoped, 401},
		{"admin token", "GET", "localhost", "/claude/" + claudeForwardDiscovery, strings.Repeat("a", 64), 401},
		{"untrusted Host", "GET", "evil.example", "/claude/" + claudeForwardDiscovery, f.token, 403},
		{"wrong method", "POST", "localhost", "/claude/" + claudeForwardDiscovery, f.token, 405},
		{"unknown route", "GET", "localhost", "/unknown/" + claudeForwardDiscovery, f.token, 401},
		{"traversal", "GET", "localhost", "/claude/../claude/" + claudeForwardDiscovery, f.token, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Host = tt.host
			req.Header.Set("X-Scoped-Key", "Local "+tt.token)
			rr := httptest.NewRecorder()
			if !f.service.claudeForward(rr, req) || rr.Code != tt.want {
				t.Fatal(rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "CERTIFICATE") || strings.Contains(rr.Body.String(), f.token) {
				t.Fatal("bootstrap disclosed trust or credential to rejected client")
			}
		})
	}
	// Explicit PublicURL is checked against Host, then normalized to an origin;
	// an operator's mount/query must not leak into the native HTTPS_PROXY URL.
	f.service.opts.PublicURL = "https://proxy.example:9443/admin-mount/?discard=yes"
	req := httptest.NewRequest("GET", "/claude/"+claudeForwardDiscovery, nil)
	req.Host = "proxy.example:9443"
	req.Header.Set("X-Scoped-Key", "Local "+f.token)
	req.Header.Set("Forwarded", "host=evil.example;proto=http")
	rr := httptest.NewRecorder()
	f.service.claudeForward(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"proxy_url":"https://proxy.example:9443"`) {
		t.Fatal(rr.Code, rr.Body.String())
	}
	req.Host = "evil.example"
	rr = httptest.NewRecorder()
	f.service.claudeForward(rr, req)
	if rr.Code != 403 {
		t.Fatal("PublicURL host not checked", rr.Code)
	}
	f.service.opts.PublicURL = ""
	for _, kind := range []string{"disabled", "wrong kind", "no account", "wrong upstream", "explicit port", "base path", "userinfo"} {
		t.Run(kind, func(t *testing.T) {
			// Directly mutate the test snapshot so malformed trusted configurations are
			// rejected here even when ordinary activation would reject them earlier.
			original := f.service.snap.routes[0]
			defer func() { f.service.snap.routes[0] = original }()
			ar := original
			switch kind {
			case "disabled":
				ar.Enabled = false
			case "wrong kind":
				// Account kind is immutable through the normal store API.
				if _, err := f.service.store.db.Exec("UPDATE accounts SET kind='chatgpt' WHERE name='claude'"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := f.service.store.db.Exec("UPDATE accounts SET kind='claude-code' WHERE name='claude'"); err != nil {
						t.Error("restore account kind:", err)
					}
				}()
			case "no account":
				ar.Account = ""
			case "wrong upstream":
				ar.Upstream = "https://evil.example"
			case "explicit port":
				ar.Upstream = "https://" + claudeAPIHost + ":443"
			case "base path":
				ar.Upstream = "https://" + claudeAPIHost + "/api"
			case "userinfo":
				ar.Upstream = "https://user@" + claudeAPIHost
			}
			f.service.snap.routes[0] = ar
			req := httptest.NewRequest("GET", "/claude/"+claudeForwardDiscovery, nil)
			req.Host = "localhost"
			req.Header.Set("X-Scoped-Key", "Local "+f.token)
			rr := httptest.NewRecorder()
			f.service.claudeForward(rr, req)
			if rr.Code != 401 {
				t.Fatal("ineligible route bootstrap", rr.Code)
			}
			_, _, status := f.connect(t, claudeAPIHost+":443", claudeForwardBasic("claude", f.token))
			if status != 407 {
				t.Fatal("ineligible route tunnel", status)
			}
		})
	}
	if f.vendorCalls.Load() != 0 {
		t.Fatal("bootstrap performed vendor I/O")
	}
}

func TestClaudeForwardUnauthorizedDoesNotCreateCA(t *testing.T) {
	s := testService(t)
	req := httptest.NewRequest("GET", "/claude/"+claudeForwardDiscovery, nil)
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	s.claudeForward(rr, req)
	if rr.Code != 401 {
		t.Fatal(rr.Code)
	}
	if s.claudeNative != nil {
		t.Fatal("unauthorized request initialized trust")
	}
	if _, err := os.Lstat(filepath.Join(s.opts.StateDir, claudeForwardCAFile)); !os.IsNotExist(err) {
		t.Fatal("unauthorized request accessed CA state", err)
	}
	req = httptest.NewRequest("GET", "http://evil.example/v1/messages", nil)
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatal("plain HTTP absolute forwarding", rr.Code)
	}
}

func TestClaudeForwardCAServiceRestart(t *testing.T) {
	f := newClaudeForwardFixture(t)
	first := f.bootstrap(t, f.token)
	opts := f.service.opts
	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest("GET", "/claude/"+claudeForwardDiscovery, nil)
	req.Host = "localhost"
	req.Header.Set("X-Scoped-Key", "Local "+f.token)
	rr := httptest.NewRecorder()
	s.claudeForward(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), strings.ReplaceAll(first.CertificatePEM, "\n", `\n`)) {
		t.Fatal("service restart changed CA", rr.Code, rr.Body.String())
	}
}

func TestClaudeForwardNativeRequestLimits(t *testing.T) {
	f := newClaudeForwardFixture(t)
	f.service.opts.MaxBody = 8
	conn, reader := f.native(t)
	if code, _ := claudeForwardNativeCall(t, conn, reader, "POST", "/v1/messages", claudeAPIHost, f.access, ""); code != 413 {
		t.Fatal("body limit bypassed", code)
	}
	conn.Close()
	claudeForwardWaitSockets(t, f.service)
	conn, reader = f.native(t)
	if _, err := fmt.Fprintf(conn, "GET /v1/models HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nX-Large: %s\r\n\r\n", claudeAPIHost, f.access, strings.Repeat("x", 80<<10)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 431 {
		t.Fatal("header limit bypassed", resp.StatusCode)
	}
	if f.vendorCalls.Load() != 0 {
		t.Fatal("oversize native request dispatched")
	}
}

// Buffer both the CONNECT request and first TLS record in one network write.
// This exercises Hijack's buffered reader instead of relying on packet timing.
type claudeForwardPipelinedClient struct {
	net.Conn
	prefix   string
	reader   *bufio.Reader
	wrote    bool
	accepted bool
}

func (c *claudeForwardPipelinedClient) Write(p []byte) (int, error) {
	if c.wrote {
		return c.Conn.Write(p)
	}
	c.wrote = true
	combined := append([]byte(c.prefix), p...)
	n, err := c.Conn.Write(combined)
	if n < len(c.prefix) {
		return 0, err
	}
	return n - len(c.prefix), err
}
func (c *claudeForwardPipelinedClient) Read(p []byte) (int, error) {
	if !c.accepted {
		c.accepted = true
		resp, err := http.ReadResponse(c.reader, &http.Request{Method: "CONNECT"})
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != 200 {
			return 0, fmt.Errorf("CONNECT status %d", resp.StatusCode)
		}
	}
	return c.reader.Read(p)
}

func TestClaudeForwardPreservesBufferedClientHello(t *testing.T) {
	f := newClaudeForwardFixture(t)
	conn, err := net.DialTimeout("tcp", f.server.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	prefix := fmt.Sprintf("CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n%s\r\n", claudeAPIHost, claudeAPIHost, claudeForwardBasic("claude", f.token))
	pipelined := &claudeForwardPipelinedClient{Conn: conn, prefix: prefix, reader: bufio.NewReader(conn)}
	secure := tls.Client(pipelined, &tls.Config{ServerName: claudeAPIHost, RootCAs: f.roots, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		t.Fatal("buffered ClientHello lost", err)
	}
	defer secure.Close()
	if code, _ := claudeForwardNativeCall(t, secure, bufio.NewReader(secure), "GET", "/v1/models", claudeAPIHost, f.access, ""); code != 200 {
		t.Fatal(code)
	}
}

func TestClaudeForwardScopeGuardBlocksPrefixReassignment(t *testing.T) {
	f := newClaudeForwardFixture(t)
	original := f.service.snap.routes[0]
	other := original.Route
	other.Name = "other"
	other.LocalPrefix = "/other/"
	configure(t, f.service, Config{Routes: []Route{original.Route, other}}, nil)
	token := caller(t, f.service, "claude", "other")
	req := httptest.NewRequest("GET", "/other/v1/models", nil)
	req.Header.Set("X-Scoped-Key", "Local "+token)
	req = req.WithContext(context.WithValue(req.Context(), claudeForwardScopeKey{}, claudeForwardScope{route: original.Name, authority: original.authority}))
	rr := httptest.NewRecorder()
	f.service.forward(rr, req)
	if rr.Code != 401 || f.vendorCalls.Load() != 0 {
		t.Fatal("native request rebound to another granted route", rr.Code)
	}
	records, _, err := f.service.store.records(nil, 10)
	if err != nil || len(records) != 0 {
		t.Fatal("scope mismatch entered request ledger", err)
	}
}
