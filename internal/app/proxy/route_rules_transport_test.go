package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouteRulesRedirectPolicy(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "https://not-allowed.invalid/private")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer up.Close()
	s := testService(t)
	r := rulesRoute(t, up.URL)
	r.Redirects = "refuse"
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "vendor")})
	token := caller(t, s, r.Name)
	for _, rule := range r.Rules {
		w := rulesRequest(s, "GET", r.LocalPrefix+strings.TrimPrefix(rule.PathPrefix, "/")+"endpoint", rule.CallerAuth, token)
		if w.Code != 502 || w.Header().Get("Location") != "" || w.Header().Get("X-Term-LLM-Proxy-Redirect") != "refused" {
			t.Fatalf("%s: %d %v", rule.PathPrefix, w.Code, w.Header())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("redirects retried/followed: %d calls", calls.Load())
	}
}

func TestRouteRulesWebSocketSelectedCredentials(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/go/v1/socket" || r.Header.Get("X-Goog-Api-Key") != "vendor" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("wrong WS selection/auth: %s %v", r.URL, r.Header)
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.CopyN(conn, rw, 4)
	}))
	defer up.Close()
	s := testService(t)
	r := rulesRoute(t, up.URL)
	r.WebSocket = true
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "vendor")})
	token := caller(t, s, r.Name)
	done := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ServeHTTP(w, r)
		close(done)
	}))
	defer proxy.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET %sgemini/socket HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nX-Goog-Api-Key: %s\r\nX-Api-Key: %s\r\nAuthorization: Bearer %s\r\n\r\n", r.LocalPrefix, token, token, token)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("WS status: %d", resp.StatusCode)
	}
	wire := []byte{0x88, 0x02, 0x03, 0xe8}
	if _, err := conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(wire))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != string(wire) {
		t.Fatalf("WS wire changed: %v %v", got, err)
	}
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WS forwarding did not finish")
	}
	records, _, err := s.store.records(nil, 10)
	if err != nil || len(records) != 1 || records[0].Route != r.Name || records[0].Status != 101 {
		t.Fatalf("parent WS accounting: %+v %v", records, err)
	}
}

func TestRouteRulesValidateAllAccountDestinations(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer up.Close()
	s := testService(t)
	testAccount(t, s, "chatgpt", up, false)
	r := rulesRoute(t, up.URL)
	r.Account = "account"
	for i := range r.Rules {
		r.Rules[i].SetHeaders = nil
	}
	compileAccount := func() *snapshot {
		t.Helper()
		snap, err := compile(fixtureProviders(Config{Routes: []Route{r}}), 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	if err := s.validateAccounts(compileAccount()); err != nil {
		t.Fatal(err)
	}
	r.Rules[2].Upstream = "https://untrusted.invalid/base"
	if err := s.validateAccounts(compileAccount()); err == nil {
		t.Fatal("last destination escaped account origin scope")
	}
	r.Rules[2].Upstream = up.URL
	value := "static-vendor-key"
	r.Rules[2].SetHeaders = map[string]HeaderRule{"Authorization": {Value: &value}}
	if err := s.validateAccounts(compileAccount()); err == nil {
		t.Fatal("rule overrode account-controlled header")
	}
}
