package proxy

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	claudeForwardDiscovery        = ".well-known/term-llm/claude-proxy"
	claudeForwardLifetime         = time.Hour
	claudeForwardHandshakeTimeout = 15 * time.Second
	claudeForwardIdleTimeout      = 90 * time.Second
)

// claudeForwarder is deliberately separate from request admission: retaining a
// normal forwarding slot during CONNECT would deadlock MaxConcurrent=1.
// Service owns one forwarder per provider, guarded by s.mu.
// All long-lived work is registered in the existing socket cancellation set.
type claudeForwarder struct {
	ca           *claudeForwardCA
	provider     *nativeProxyProvider
	slots        chan struct{}
	certificates claudeForwardCertificates
}

// claudeForwarder lazily initializes service-private trust material. Callers
// authenticate and take a short-lived request slot before invoking it. There is
// no process-global cache or installation into the operating system trust store.
func (s *Service) claudeForwarder() (*claudeForwarder, error) {
	return s.nativeForwarder(claudeProxyProvider)
}

func (s *Service) nativeForwarder(provider *nativeProxyProvider) (*claudeForwarder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, fmt.Errorf("proxy shutting down")
	}
	slot := &s.claudeNative
	if provider == agyProxyProvider {
		slot = &s.agyNative
	}
	if *slot == nil {
		ca, err := provider.openCA(s.opts.StateDir)
		if err != nil {
			return nil, err
		}
		*slot = &claudeForwarder{ca: ca, provider: provider, slots: make(chan struct{}, cap(s.slots))}
	}
	return *slot, nil
}

// claudeForward runs before ordinary ServeHTTP dispatch. It consumes all
// CONNECT requests (including rejected targets) and the reserved per-route
// bootstrap endpoint. Absolute-form data forwarding remains rejected by forward;
// non-forwarding administrative dispatch is unchanged. This never recursively
// invokes ServeHTTP.
func (s *Service) claudeForward(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodConnect {
		s.claudeForwardConnect(w, r)
		return true
	}
	for _, provider := range nativeProxyProviders {
		if strings.HasSuffix(r.URL.Path, "/"+provider.discovery) {
			s.claudeForwardBootstrap(w, r, provider)
			return true
		}
	}
	return false
}

func (s *Service) claudeForwardRequestSlot(w http.ResponseWriter) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		http.Error(w, "proxy concurrency limit", http.StatusServiceUnavailable)
		return false
	}
}

// claudeForwardRouteLocked performs no inference, token refresh or vendor I/O.
// The service lock must cover authorization and socket registration, matching
// the lifecycle fence in websocket.go and ordinary forwarding.
func (s *Service) claudeForwardRouteLocked(name, token string) (activeRoute, Caller, error) {
	return s.nativeForwardRouteLocked(name, token, claudeProxyProvider)
}

func (s *Service) nativeForwardRouteLocked(name, token string, provider *nativeProxyProvider) (activeRoute, Caller, error) {
	if s.closing || token == "" || len(token) > 16<<10 || subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(s.adminHash)) == 1 {
		return activeRoute{}, Caller{}, fmt.Errorf("unauthorized")
	}
	for _, ar := range s.snap.routes {
		if ar.Name != name || !ar.Enabled {
			continue
		}
		if ar.Upstream != "https://"+provider.apiHost && ar.Upstream != "https://"+provider.apiHost+"/" {
			break
		}
		if ar.Account == "" {
			break
		}
		account, err := s.store.account(ar.Account)
		if err != nil || account.Kind != provider.kind || !account.claudeShared() {
			break
		}
		caller, err := s.store.authorize(token, ar)
		if err != nil {
			break
		}
		return ar, caller, nil
	}
	return activeRoute{}, Caller{}, fmt.Errorf("unauthorized or route not granted")
}

func (s *Service) claudeForwardBootstrap(w http.ResponseWriter, r *http.Request, provider *nativeProxyProvider) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.IsAbs() || !safePath(r.URL.EscapedPath()) || r.URL.RawQuery != "" {
		http.Error(w, "invalid bootstrap target", 400)
		return
	}
	if !s.claudeForwardRequestSlot(w) {
		return
	}
	defer func() { <-s.slots }()
	s.mu.RLock()
	var name, token string
	for _, ar := range s.snap.routes {
		if r.URL.EscapedPath() != ar.LocalPrefix+provider.discovery {
			continue
		}
		values := r.Header.Values(ar.CallerAuth.Header)
		if len(values) == 1 && strings.HasPrefix(values[0], ar.CallerAuth.Prefix) {
			name, token = ar.Name, strings.TrimPrefix(values[0], ar.CallerAuth.Prefix)
		}
		break
	}
	ar, caller, err := s.nativeForwardRouteLocked(name, token, provider)
	var account accountRecord
	if err == nil {
		account, err = s.store.account(ar.Account)
	}
	s.mu.RUnlock()
	if err != nil {
		http.Error(w, "unauthorized or route not granted", 401)
		return
	}
	origin, allowed := s.browserOrigin(r)
	u, err := url.Parse(origin)
	if !allowed || err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "proxy Host is not allowed", 403)
		return
	}
	// Only the service's trusted origin is advertised. Never trust forwarded
	// headers, retain an admin mount, or put the scoped token into a URL.
	proxyURL := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	f, err := s.nativeForwarder(provider)
	if err != nil {
		http.Error(w, provider.label+" proxy trust unavailable", 503)
		return
	}
	metadata := map[string]any{"certificate_pem": f.ca.certificatePEM, "proxy_url": proxyURL, "route": name, "protocol_version": 2, "token_mode": "vault", "hosts": provider.hosts}
	if !s.claudeSharedBootstrap(w, r, f, account, nativeForwardScope(ar, caller, account, provider), metadata) {
		return
	}
	jsonReply(w, 200, metadata)
}

type claudeNativeTunnel struct {
	forwarder        *claudeForwarder
	route, token, id string
	host             string
	ctx              context.Context
	cancel           context.CancelCauseFunc
}

func (s *Service) claudeForwardAdmit(w http.ResponseWriter, r *http.Request) *claudeNativeTunnel {
	provider := nativeProviderForAuthority(r.Host)
	if r.ProtoMajor != 1 || r.ProtoMinor != 1 || provider == nil || r.URL.Host != r.Host ||
		r.URL.Scheme != "" || r.URL.User != nil || r.URL.Path != "" || r.URL.RawQuery != "" || r.URL.Fragment != "" ||
		r.ContentLength > 0 || len(r.TransferEncoding) > 0 || len(r.Header.Values("Upgrade")) > 0 {
		http.Error(w, "only exact native HTTPS authorities are supported", 400)
		return nil
	}
	values := r.Header.Values("Proxy-Authorization")
	var name, token string
	if len(values) == 1 && strings.HasPrefix(values[0], "Basic ") && len(values[0]) <= 24<<10 {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(values[0], "Basic "))
		if err == nil {
			name, token, _ = strings.Cut(string(raw), ":")
		}
	}
	if !slug.MatchString(name) || strings.ContainsAny(token, ": \t\r\n") {
		w.Header().Set("Proxy-Authenticate", `Basic realm="term-llm Claude"`)
		http.Error(w, "proxy authentication required", 407)
		return nil
	}
	if !s.claudeForwardRequestSlot(w) {
		return nil
	}
	defer func() { <-s.slots }()
	s.mu.RLock()
	_, _, err := s.nativeForwardRouteLocked(name, token, provider)
	s.mu.RUnlock()
	if err != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="term-llm Claude"`)
		http.Error(w, "proxy authentication required", 407)
		return nil
	}
	f, err := s.nativeForwarder(provider)
	if err != nil {
		http.Error(w, provider.label+" proxy trust unavailable", 503)
		return nil
	}
	select {
	case f.slots <- struct{}{}:
	default:
		http.Error(w, provider.label+" tunnel concurrency limit", 503)
		return nil
	}
	s.mu.Lock()
	ar, caller, err := s.nativeForwardRouteLocked(name, token, provider)
	if err != nil {
		s.mu.Unlock()
		<-f.slots
		http.Error(w, "proxy authentication required", 407)
		return nil
	}
	ctx, lifetimeCancel := context.WithTimeout(r.Context(), claudeForwardLifetime)
	ctx, cancel := context.WithCancelCause(ctx)
	id := randomToken("native_")
	s.sockets.Store(id, &activeSocket{caller: caller.ID, route: ar.Name, authority: ar.authority, account: ar.Account, cancel: cancel})
	s.socketWG.Add(1)
	s.mu.Unlock()
	return &claudeNativeTunnel{forwarder: f, route: name, token: token, id: id, host: strings.TrimSuffix(r.Host, ":443"), ctx: ctx, cancel: func(cause error) { cancel(cause); lifetimeCancel() }}
}

func (s *Service) claudeForwardConnect(w http.ResponseWriter, r *http.Request) {
	tunnel := s.claudeForwardAdmit(w, r)
	if tunnel == nil {
		return
	}
	defer func() {
		tunnel.cancel(nil)
		s.sockets.Delete(tunnel.id)
		<-tunnel.forwarder.slots
		s.socketWG.Done()
	}()
	raw, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "CONNECT requires HTTP/1 hijacking", 500)
		return
	}
	defer raw.Close()
	stop := context.AfterFunc(tunnel.ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(claudeForwardHandshakeTimeout))
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	// Hijack may already have buffered the first TLS records. Discarding that
	// reader corrupts pipelined CONNECT+ClientHello handshakes.
	buffered := &claudeForwardBufferedConn{Conn: raw, reader: rw.Reader, done: make(chan struct{})}
	conn := tls.Server(buffered, &tls.Config{
		MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return tunnel.forwarder.certificate(s.opts.StateDir)
		},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello.ServerName != tunnel.host {
				return nil, fmt.Errorf("unsupported TLS server name")
			}
			return nil, nil
		},
	})
	if err := conn.HandshakeContext(tunnel.ctx); err != nil {
		return
	}
	_ = raw.SetDeadline(time.Time{})
	s.claudeForwardServe(conn, buffered.done, tunnel)
}

func (s *Service) claudeForwardServe(conn *tls.Conn, done <-chan struct{}, tunnel *claudeNativeTunnel) {
	listener := &claudeForwardListener{conn: conn, done: done}
	closed := make(chan struct{})
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.claudeForwardNative(w, r, tunnel) }),
		BaseContext:       func(net.Listener) context.Context { return tunnel.ctx },
		ReadHeaderTimeout: claudeForwardHandshakeTimeout, IdleTimeout: claudeForwardIdleTimeout,
		ReadTimeout: s.opts.RequestTimeout, WriteTimeout: s.opts.RequestTimeout, MaxHeaderBytes: 64 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				close(closed)
			}
		},
	}
	defer server.Close()
	_ = server.Serve(listener)
	// Serve returns when Accept unblocks, potentially while a canceled forward
	// is finalizing its ledger. StateClosed fences handler completion before the
	// service socketWG can release Close to shut down SQLite.
	<-closed
}

func (s *Service) claudeForwardNative(w http.ResponseWriter, r *http.Request, tunnel *claudeNativeTunnel) {
	if r.ProtoMajor != 1 || r.ProtoMinor != 1 || (r.Host != tunnel.host && r.Host != tunnel.host+":443") ||
		r.Method == http.MethodConnect || r.URL.IsAbs() || r.URL.Host != "" || r.URL.User != nil ||
		!strings.HasPrefix(r.RequestURI, "/") || strings.HasPrefix(r.URL.EscapedPath(), "//") || !safePath(r.URL.EscapedPath()) ||
		len(r.Header.Values("Upgrade")) > 0 || hasConnectionToken(r.Header, "upgrade") {
		w.Header().Set("Connection", "close")
		http.Error(w, "unsupported native request target", 400)
		return
	}
	if len(r.Header.Values("Proxy-Authorization")) != 0 {
		http.Error(w, "unauthorized native request", 401)
		return
	}
	s.mu.RLock()
	provider := tunnel.forwarder.nativeProvider()
	ar, caller, err := s.nativeForwardRouteLocked(tunnel.route, tunnel.token, provider)
	var account accountRecord
	if err == nil {
		account, err = s.store.account(ar.Account)
	}
	s.mu.RUnlock()
	if err != nil {
		http.Error(w, "unauthorized or route not granted", 401)
		return
	}
	scope := nativeForwardScope(ar, caller, account, provider)
	if provider == claudeProxyProvider && claudeNativeProbe(tunnel.host, r) {
		s.claudeForwardProbe(w, r, tunnel.host)
		return
	}
	if tunnel.host != provider.apiHost && tunnel.host != agyProfileHost {
		if "https://"+tunnel.host+r.URL.EscapedPath() != provider.tokenURL || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Method != http.MethodPost {
			http.Error(w, "unsupported native OAuth request", 403)
			return
		}
		s.claudeVaultOAuth(w, r, tunnel, scope)
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		http.Error(w, "unauthorized native request", 401)
		return
	}
	scope.access = strings.TrimPrefix(values[0], "Bearer ")
	if (account.State != "ready" && account.State != "refreshing") || !validClaudeSharedAlias(tunnel.forwarder.ca.vaultKey, account, scope, provider.accessPrefix) {
		http.Error(w, "native login required", http.StatusUnauthorized)
		return
	}
	if provider == agyProxyProvider && tunnel.host == agyProfileHost {
		s.agyNativeProfile(w, r, tunnel.forwarder.ca.vaultKey, scope)
		return
	}
	if !provider.allowsNativeAPI(r) {
		http.Error(w, "unsupported "+provider.label+" API request", http.StatusForbidden)
		return
	}

	// Both native paths and the joined local mount are validated, without cleaning
	// or decoding away escapes. Ordinary forward retains its policy/ledger/body
	// limits; account transport resolves this scoped vault placeholder to OAuth.
	escaped := ar.LocalPrefix + strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if !safePath(escaped) {
		http.Error(w, "invalid native path", 400)
		return
	}
	mapped := r.Clone(context.WithValue(r.Context(), claudeForwardScopeKey{}, scope))
	mapped.URL.Path, _ = url.PathUnescape(escaped)
	mapped.URL.RawPath = escaped
	mapped.RequestURI = mapped.URL.RequestURI()
	mapped.Header.Del("Authorization")
	mapped.Header.Del("Proxy-Authorization")
	mapped.Header.Set(ar.CallerAuth.Header, ar.CallerAuth.Prefix+tunnel.token)
	s.forward(w, mapped)
}

// claudeForwardRouteMatches must be checked by forward after selecting a route,
// under its existing s.mu read lock and before authorization/ledger admission.
// Ordinary requests are unaffected. Native requests cannot be rebound to a
// different granted route by a concurrent local-prefix reassignment.
func claudeForwardRouteMatches(r *http.Request, ar activeRoute) bool {
	scope, bound := r.Context().Value(claudeForwardScopeKey{}).(claudeForwardScope)
	return !bound || scope.route == ar.Name && scope.authority == ar.authority
}

type claudeForwardScopeKey struct{}
type claudeForwardScope struct {
	route, authority, caller, account, access string
	identity                                  int64
	provider                                  *nativeProxyProvider
}

func nativeForwardScope(ar activeRoute, caller Caller, account accountRecord, provider *nativeProxyProvider) claudeForwardScope {
	return claudeForwardScope{route: ar.Name, authority: ar.authority, caller: caller.ID, account: ar.Account, identity: account.identityVersion, provider: provider}
}

func claudeForwardAuthority(authority string) bool {
	return nativeProviderForAuthority(authority) == claudeProxyProvider
}

type claudeForwardBufferedConn struct {
	net.Conn
	reader *bufio.Reader
	done   chan struct{}
	once   sync.Once
}

func (c *claudeForwardBufferedConn) Read(p []byte) (int, error) {
	// The hijacked reader wraps the *outer* HTTP server's connReader. Drain
	// only its already-buffered bytes, then bypass it: inner HTTP keepalive
	// probes use read deadlines, and passing those through the outer connReader
	// would cancel the CONNECT request context on an ordinary probe timeout.
	if n := c.reader.Buffered(); n > 0 {
		if len(p) > n {
			p = p[:n]
		}
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
func (c *claudeForwardBufferedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

type claudeForwardListener struct {
	conn     net.Conn
	done     <-chan struct{}
	accepted bool
}

func (l *claudeForwardListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *claudeForwardListener) Close() error   { return l.conn.Close() }
func (l *claudeForwardListener) Addr() net.Addr { return l.conn.LocalAddr() }
