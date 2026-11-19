package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var claudeForwardProbeTargets = []struct{ host, path string }{
	{claudeAPIHost, "/api/hello"},
	{claudeOAuthHost, "/v1/oauth/hello"},
}

func newClaudeForwardProbeFixture(t *testing.T) *claudeForwardFixture {
	t.Helper()
	f := newClaudeForwardFixture(t)
	// Simulate a fresh operator profile. Its scoped grant permits startup
	// probes before the shared account has completed enrollment.
	prepareClaudeSharedEnrollment(t, f)
	claudeForwardProbeAssertNoLogin(t, f)
	return f
}

func claudeForwardProbeAssertNoLogin(t *testing.T, f *claudeForwardFixture) {
	t.Helper()
	account, err := f.service.store.account("claude")
	if err != nil || account.State != "needs_login" || len(account.claudeSharedCipher) != 0 || account.credentials.AccessToken != "" || account.credentials.RefreshToken != "" {
		t.Fatalf("unexpected shared account OAuth credentials: state=%q err=%v", account.State, err)
	}
	if f.vendorCalls.Load() != 0 || f.oauthCalls.Load() != 0 {
		t.Fatalf("public probe dispatched authenticated traffic: API=%d OAuth=%d", f.vendorCalls.Load(), f.oauthCalls.Load())
	}
}

// Read the real tunneled HTTP response, preserving headers for custody checks.
// Framing is supplied explicitly so malformed probe variants are not normalized
// by an HTTP client before the native handler sees them.
func claudeForwardProbeCall(t *testing.T, conn net.Conn, reader *bufio.Reader, method, path, host, headers, body string) (*http.Response, string) {
	t.Helper()
	if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\n%s\r\n%s", method, path, host, headers, body); err != nil {
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
	return resp, string(data)
}

func TestClaudeForwardProbeFreshProfile(t *testing.T) {
	for _, target := range claudeForwardProbeTargets {
		t.Run(target.host, func(t *testing.T) {
			f := newClaudeForwardProbeFixture(t)
			// MaxConcurrent=1 is intentional: CONNECT must not retain the only
			// ordinary request slot and deadlock either startup probe.
			conn, reader := f.nativeHost(t, target.host, f.token)
			f.probe = func(w http.ResponseWriter, r *http.Request) {
				if r.RequestURI != target.path || r.UserAgent() != "claude-synthetic-startup" {
					t.Errorf("probe changed: %s %q", r.RequestURI, r.UserAgent())
				}
				for key := range r.Header {
					switch key {
					case "Accept", "Accept-Encoding", "User-Agent":
					default:
						t.Errorf("unexpected client header forwarded: %s", key)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"hello":"public-upstream"}`)
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, credentials := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/credentials=%t", method, credentials), func(t *testing.T) {
						headers := "Content-Length: 0\r\nUser-Agent: claude-synthetic-startup\r\n"
						if credentials {
							headers += "Authorization: Bearer " + f.access + "\r\nAuthorization: Basic alternate-secret\r\n" +
								"Cookie: session=private-cookie\r\nX-API-Key: private-api-key\r\nX-Scoped-Key: Local " + f.token + "\r\n" +
								"X-Auth-Token: alternate-token\r\nX-Goog-Api-Key: alternate-api-key\r\n" +
								"Anthropic-Beta: private-beta\r\nX-Private-Trace: private-trace\r\n" +
								"Accept: text/plain\r\nAccept-Encoding: gzip\r\n"
						}
						resp, body := claudeForwardProbeCall(t, conn, reader, method, target.path, target.host, headers, "")
						wantBody := `{"hello":"public-upstream"}`
						if method == http.MethodHead {
							wantBody = ""
						}
						if resp.StatusCode != 200 || body != wantBody || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
							t.Fatalf("probe did not forward actual public response: %d %v %q", resp.StatusCode, resp.Header, body)
						}
					})
				}
			}
			if f.probeCalls.Load() != 4 || len(f.service.slots) != 0 {
				t.Fatalf("probe dispatch/slot leak: calls=%d slots=%d", f.probeCalls.Load(), len(f.service.slots))
			}
			claudeForwardProbeAssertNoLogin(t, f)
		})
	}
}

func TestClaudeForwardProbeRejectsNonPublicVariants(t *testing.T) {
	for _, target := range claudeForwardProbeTargets {
		t.Run(target.host, func(t *testing.T) {
			f := newClaudeForwardProbeFixture(t)
			encoded := strings.Replace(target.path, "hello", "%68ello", 1)
			for _, tt := range []struct {
				name, method, path, headers, body string
				want                              int
			}{
				{"other path", "GET", target.path + "-private", "", "", 0},
				{"trailing slash", "GET", target.path + "/", "", "", 0},
				{"wrong host endpoint", "GET", "/v1/oauth/hello", "", "", 0},
				{"query", "GET", target.path + "?key=private", "", "", 0},
				{"empty query", "GET", target.path + "?", "", "", 0},
				{"encoded character", "GET", encoded, "", "", 0},
				{"encoded separator", "GET", strings.Replace(target.path, "/hello", "%2Fhello", 1), "", "", 0},
				{"double encoded character", "GET", strings.Replace(target.path, "hello", "%2568ello", 1), "", "", 0},
				{"traversal", "GET", "/private/../" + strings.TrimPrefix(target.path, "/"), "", "", 400},
				{"POST", "POST", target.path, "Content-Length: 0\r\n", "", 0},
				{"PUT", "PUT", target.path, "Content-Length: 0\r\n", "", 0},
				{"OPTIONS", "OPTIONS", target.path, "", "", 0},
				{"GET body", "GET", target.path, "Content-Length: 1\r\n", "x", 400},
				{"HEAD body", "HEAD", target.path, "Content-Length: 1\r\n", "x", 400},
				{"chunked empty body", "GET", target.path, "Transfer-Encoding: chunked\r\n", "0\r\n\r\n", 400},
				{"chunked body", "GET", target.path, "Transfer-Encoding: chunked\r\n", "1\r\nx\r\n0\r\n\r\n", 400},
				{"credential trailer", "GET", target.path, "Transfer-Encoding: chunked\r\nTrailer: Authorization\r\n", "0\r\nAuthorization: Bearer private-trailer\r\n\r\n", 400},
				{"inner proxy auth", "GET", target.path, claudeForwardBasic("claude", f.token), "", 401},
				{"absolute target", "GET", "https://" + target.host + target.path, "", "", 400},
			} {
				if tt.name == "wrong host endpoint" && target.host == claudeOAuthHost {
					tt.path = "/api/hello"
				}
				t.Run(tt.name, func(t *testing.T) {
					conn, reader := f.nativeHost(t, target.host, f.token)
					resp, body := claudeForwardProbeCall(t, conn, reader, tt.method, tt.path, target.host, tt.headers, tt.body)
					conn.Close()
					claudeForwardWaitSockets(t, f.service)
					if tt.want != 0 {
						if resp.StatusCode != tt.want {
							t.Fatalf("got %d want %d: %s", resp.StatusCode, tt.want, body)
						}
					} else if resp.StatusCode != 400 && resp.StatusCode != 401 && resp.StatusCode != 403 {
						t.Fatalf("non-public variant bypassed authentication: %d %s", resp.StatusCode, body)
					}
				})
			}
			if f.probeCalls.Load() != 0 {
				t.Fatal("non-public variant reached public upstream", f.probeCalls.Load())
			}
			claudeForwardProbeAssertNoLogin(t, f)
		})
	}
}

func TestClaudeForwardProbeDoesNotAuthorizeAPIWithoutLogin(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	conn, reader := f.native(t)
	if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", ""); resp.StatusCode != 200 {
		t.Fatal("startup probe", resp.StatusCode)
	}
	for _, token := range []string{"", f.access, f.token, "synthetic-vendor-access"} {
		for _, path := range []string{"/v1/models", "/v1/messages"} {
			method, body := "GET", ""
			if path == "/v1/messages" {
				method, body = "POST", `{"model":"fixture"}`
			}
			headers := fmt.Sprintf("Content-Length: %d\r\nX-API-Key: private-api-key\r\n", len(body))
			if token != "" {
				headers += "Authorization: Bearer " + token + "\r\n"
			}
			resp, data := claudeForwardProbeCall(t, conn, reader, method, path, claudeAPIHost, headers, body)
			if resp.StatusCode != 401 {
				t.Fatalf("API usable without native login: %s %d %s", path, resp.StatusCode, data)
			}
		}
	}
	claudeForwardProbeAssertNoLogin(t, f)
}

func TestClaudeForwardProbeRequiresCurrentScopedCallerAndGrant(t *testing.T) {
	for _, target := range claudeForwardProbeTargets {
		for _, kind := range []string{"caller", "grant"} {
			t.Run(target.host+"/"+kind, func(t *testing.T) {
				f := newClaudeForwardProbeFixture(t)
				for _, auth := range []string{"", claudeForwardBasic("claude", "wrong-scoped-token"), claudeForwardBasic("other-route", f.token)} {
					conn, _, status := f.connect(t, target.host+":443", auth)
					conn.Close()
					if status != 407 {
						t.Fatal("probe CONNECT accepted invalid scoped authentication", status)
					}
				}
				conn, reader := f.nativeHost(t, target.host, f.token)
				if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", target.path, target.host, "", ""); resp.StatusCode != 200 {
					t.Fatal("initial probe", resp.StatusCode)
				}
				// Skip proactive socket cancellation deliberately: every probe on
				// an existing TLS tunnel must recheck the current database grant.
				f.service.mu.Lock()
				_, principal, err := f.service.claudeForwardRouteLocked("claude", f.token)
				if err == nil {
					if kind == "grant" {
						err = f.service.store.updateGrants(principal.ID, nil, f.service.snap)
					} else {
						err = f.service.store.revokeCaller(context.Background(), principal.ID)
					}
				}
				f.service.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", target.path, target.host, "", ""); resp.StatusCode != 401 {
					t.Fatal("probe bypassed revocation on existing tunnel", resp.StatusCode)
				}
				conn.Close()
				claudeForwardWaitSockets(t, f.service)
				if _, _, status := f.connect(t, target.host+":443", claudeForwardBasic("claude", f.token)); status != 407 {
					t.Fatal("revoked caller/grant created a new probe tunnel", status)
				}
				if f.probeCalls.Load() != 1 {
					t.Fatal("revoked probe reached public upstream", f.probeCalls.Load())
				}
				claudeForwardProbeAssertNoLogin(t, f)
			})
		}
	}
}

func TestClaudeForwardProbePreservesStatusWithoutRedirectOrCookies(t *testing.T) {
	for _, target := range claudeForwardProbeTargets {
		t.Run(target.host, func(t *testing.T) {
			f := newClaudeForwardProbeFixture(t)
			conn, reader := f.nativeHost(t, target.host, f.token)
			for _, status := range []int{200, 401, 403, 429, 503, 301, 302, 307, 308} {
				t.Run(fmt.Sprint(status), func(t *testing.T) {
					f.probe = func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Set-Cookie", "vendor-session=private; Secure")
						w.Header().Set("Location", "https://"+target.host+target.path+"?redirect=private")
						w.Header().Set("Authorization", "Bearer upstream-secret")
						w.Header().Set("X-Vendor-Secret", "private-response")
						w.Header().Set("Cache-Control", "public, max-age=86400")
						w.WriteHeader(status)
						fmt.Fprintf(w, `{"public_status":%d}`, status)
					}
					before := f.probeCalls.Load()
					resp, body := claudeForwardProbeCall(t, conn, reader, "GET", target.path, target.host, "Cookie: worker=private\r\n", "")
					if resp.StatusCode != status || body != fmt.Sprintf(`{"public_status":%d}`, status) || f.probeCalls.Load() != before+1 {
						t.Fatalf("upstream status/body changed or redirect followed: %d %q calls=%d", resp.StatusCode, body, f.probeCalls.Load()-before)
					}
					for _, key := range []string{"Set-Cookie", "Location", "Authorization", "X-Vendor-Secret"} {
						if len(resp.Header.Values(key)) != 0 {
							t.Errorf("probe copied upstream %s", key)
						}
					}
					if resp.Header.Get("Cache-Control") != "no-store" {
						t.Fatal("probe copied upstream cache policy")
					}
				})
			}
			claudeForwardProbeAssertNoLogin(t, f)
		})
	}
}

func TestClaudeForwardProbeBoundsIdentityResponse(t *testing.T) {
	const maxReply = 64 << 10 // Contract, deliberately independent of production constant.
	for _, tt := range []struct {
		name     string
		size     int
		encoding string
		want     int
	}{
		{"exact limit", maxReply, "", 200},
		{"over limit", maxReply + 1, "", 502},
		{"compressed reply", 16, "gzip", 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newClaudeForwardProbeFixture(t)
			f.probe = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.encoding != "" {
					w.Header().Set("Content-Encoding", tt.encoding)
				}
				io.WriteString(w, strings.Repeat("x", tt.size))
			}
			conn, reader := f.native(t)
			resp, body := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", "")
			if resp.StatusCode != tt.want || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("invalid response handling: %d %v", resp.StatusCode, resp.Header)
			}
			if tt.want == 200 && body != strings.Repeat("x", tt.size) || tt.want != 200 && len(body) >= maxReply {
				t.Fatalf("unbounded or changed response: length=%d", len(body))
			}
			if len(f.service.slots) != 0 {
				t.Fatal("response validation leaked request slot")
			}
			claudeForwardProbeAssertNoLogin(t, f)
		})
	}
}

func TestClaudeForwardProbeStopsOversizeStreamWithoutEOF(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	closed := make(chan struct{})
	f.probe = func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		io.WriteString(w, strings.Repeat("x", (64<<10)+1))
		w.(http.Flusher).Flush()
		// There is deliberately no EOF until the proxy closes the bounded body.
		<-r.Context().Done()
	}
	conn, reader := f.native(t)
	resp, body := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", "")
	if resp.StatusCode != 502 || len(body) >= 64<<10 {
		t.Fatalf("oversize stream not bounded: %d length=%d", resp.StatusCode, len(body))
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("oversize upstream body not closed")
	}
	claudeForwardProbeAssertNoLogin(t, f)
}

func TestClaudeForwardProbeUpstreamFailuresReleaseSlot(t *testing.T) {
	for _, kind := range []string{"transport", "truncated body"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeForwardProbeFixture(t)
			dial := f.service.transport.DialContext
			if kind == "transport" {
				f.service.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
					return nil, fmt.Errorf("synthetic-private-upstream-failure")
				}
			} else {
				f.probe = func(w http.ResponseWriter, r *http.Request) {
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nprivate-truncated-response")
					rw.Flush()
				}
			}
			conn, reader := f.native(t)
			resp, body := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", "")
			if resp.StatusCode != 502 || strings.Contains(body, "private-") || len(f.service.slots) != 0 {
				t.Fatalf("unsafe upstream failure: %d %q slots=%d", resp.StatusCode, body, len(f.service.slots))
			}
			f.service.transport.DialContext = dial
			f.probe = nil
			if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", ""); resp.StatusCode != 200 {
				t.Fatal("failed probe leaked the only request slot", resp.StatusCode)
			}
			claudeForwardProbeAssertNoLogin(t, f)
		})
	}
}

func TestClaudeForwardProbeHonorsRequestAdmission(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	conn, reader := f.native(t)
	f.service.slots <- struct{}{}
	resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", "")
	<-f.service.slots
	if resp.StatusCode != 503 || f.probeCalls.Load() != 0 {
		t.Fatal("probe bypassed ordinary request concurrency limit", resp.StatusCode)
	}
	if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", "/api/hello", claudeAPIHost, "", ""); resp.StatusCode != 200 {
		t.Fatal("probe deadlocked at MaxConcurrent=1", resp.StatusCode)
	}
	claudeForwardProbeAssertNoLogin(t, f)
}

func TestClaudeForwardProbeHonorsRequestTimeout(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	f.service.opts.RequestTimeout = 100 * time.Millisecond
	closed := make(chan struct{})
	f.probe = func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(closed)
	}
	// Use the native dispatch directly for this one assertion: the tunneled
	// HTTP server has its own equal WriteTimeout, which may close the client
	// before a 502 can be written. We are testing the upstream request deadline.
	forwarder, err := f.service.claudeForwarder()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/hello", nil).WithContext(ctx)
	req.Host = claudeAPIHost
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.service.claudeForwardNative(rr, req, &claudeNativeTunnel{forwarder: forwarder, route: "claude", token: f.token, host: claudeAPIHost})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("probe ignored the configured upstream request timeout")
	}
	if rr.Code != 502 || len(f.service.slots) != 0 {
		t.Fatalf("upstream timeout not enforced: %d slots=%d", rr.Code, len(f.service.slots))
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed-out probe left upstream active")
	}
	claudeForwardProbeAssertNoLogin(t, f)
}

func TestClaudeForwardProbeNativeStartupThenLoginAndAPI(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	for _, target := range claudeForwardProbeTargets {
		conn, reader := f.nativeHost(t, target.host, f.token)
		if resp, _ := claudeForwardProbeCall(t, conn, reader, "GET", target.path, target.host, "", ""); resp.StatusCode != 200 {
			t.Fatal("pre-login startup probe", target.host, resp.StatusCode)
		}
		conn.Close()
		claudeForwardWaitSockets(t, f.service)
	}
	claudeForwardProbeAssertNoLogin(t, f)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != "POST" || r.URL.Path != "/v1/oauth/token" || string(body) != claudeVaultCodePayload || f.probeCalls.Load() != 2 {
			t.Errorf("code exchange changed or preceded startup probes: %s %s %v", r.Method, body, err)
		}
		io.WriteString(w, `{"access_token":"synthetic-vendor-access","refresh_token":"synthetic-vendor-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference"}`)
	}
	conn, reader := f.nativeHost(t, claudeOAuthHost, f.token)
	headers := fmt.Sprintf("Content-Type: application/json\r\nContent-Length: %d\r\n", len(claudeVaultCodePayload))
	resp, body := claudeForwardProbeCall(t, conn, reader, "POST", "/v1/oauth/token", claudeOAuthHost, headers, claudeVaultCodePayload)
	if resp.StatusCode != 200 || strings.Contains(body, "synthetic-vendor") {
		t.Fatalf("unsafe startup code exchange: %d %s", resp.StatusCode, body)
	}
	var reply claudeVaultReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.AccessToken, "sk-ant-oat01-tlv2-") {
		t.Fatal("code exchange did not return an opaque access placeholder")
	}
	conn.Close()
	claudeForwardWaitSockets(t, f.service)
	api, apiReader := f.native(t)
	if code, _ := claudeForwardNativeCall(t, api, apiReader, "POST", "/v1/messages", claudeAPIHost, reply.AccessToken, ""); code != 200 {
		t.Fatal("authenticated inference after startup/login", code)
	}
	if f.probeCalls.Load() != 2 || f.oauthCalls.Load() != 1 || f.vendorCalls.Load() != 1 {
		t.Fatalf("unexpected startup dispatches: probes=%d OAuth=%d API=%d", f.probeCalls.Load(), f.oauthCalls.Load(), f.vendorCalls.Load())
	}
	records, _, err := f.service.store.records(nil, 100)
	if err != nil || len(records) != 1 || records[0].Path != "/v1/messages" {
		t.Fatalf("public startup probes entered inference ledger: records=%v err=%v", records, err)
	}
}
