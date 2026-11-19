package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestChatGPTAccountLiveSideband(t *testing.T) {
	const sidebandPath = "/v1/live/rtc_fixture"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-old-access" || r.Header.Get("ChatGPT-Account-ID") != "synthetic-account-id" {
			t.Error("proxy did not substitute ChatGPT account credentials")
		}
		switch {
		case r.Host == "chatgpt.com" && r.Method == http.MethodPost && r.URL.Path == "/backend-api/codex/realtime/calls":
			w.Header().Set("Location", sidebandPath)
			w.WriteHeader(http.StatusCreated)
		case r.Host == "api.openai.com" && r.URL.Path == sidebandPath:
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created"}`)); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected upstream request: %s %s%s", r.Method, r.Host, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	s := testService(t)
	a := testAccount(t, s, "chatgpt", upstream, false)
	// Exercise the real vendor-fixed policy, not the custom-origin fixture bypass.
	a.operator = accountOperator{}
	if err := s.store.replaceAccount(context.Background(), a, a.Generation); err != nil {
		t.Fatal(err)
	}
	s.transport.TLSClientConfig.ServerName = upstream.Listener.Addr().(*net.TCPAddr).IP.String()
	s.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "chatgpt.com:443" && addr != "api.openai.com:443" {
			return nil, fmt.Errorf("unexpected upstream address %q", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	chat, _ := routePresetByID("chatgpt")
	sideband, _ := routePresetByID("chatgpt-openai")
	configure(t, s, Config{Routes: []Route{chatGPTPresetRoute(chat, chat.ID, a.Name), chatGPTPresetRoute(sideband, sideband.ID, a.Name)}}, nil)
	token := caller(t, s, chat.ID, sideband.ID)

	req := httptest.NewRequest(http.MethodPost, "/chatgpt/realtime/calls", strings.NewReader("offer"))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated || rr.Header().Get("Location") != sidebandPath {
		t.Fatalf("create call: HTTP %d, location %q", rr.Code, rr.Header().Get("Location"))
	}

	server := httptest.NewServer(s)
	defer server.Close()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/chatgpt-openai" + strings.TrimPrefix(sidebandPath, "/v1")
	conn, resp, err := dialer.Dial(endpoint, http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("sideband upgrade: HTTP %d: %v: %s", resp.StatusCode, err, body)
		}
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, message, err := conn.ReadMessage(); err != nil || string(message) != `{"type":"session.created"}` {
		t.Fatalf("sideband event: %q, %v", message, err)
	}
}

func TestChatGPTAccountLiveSidebandScope(t *testing.T) {
	account := accountRecord{AccountInfo: AccountInfo{Kind: "chatgpt"}}
	for _, tc := range []struct {
		name, host, method, path, allow string
		upgrade, websocket, want        bool
	}{
		{"call", "api.openai.com", "GET", "/live/rtc_fixture", "GET /live/", true, true, true},
		{"plain HTTP", "api.openai.com", "GET", "/live/rtc_fixture", "", false, true, false},
		{"wrong method", "api.openai.com", "POST", "/live/rtc_fixture", "", true, true, false},
		{"empty ID", "api.openai.com", "GET", "/live/", "", true, true, false},
		{"nested path", "api.openai.com", "GET", "/live/rtc_fixture/extra", "", true, true, false},
		{"escaped path", "api.openai.com", "GET", "/live/%72tc_fixture", "", true, true, false},
		{"wrong origin", "chatgpt.com", "GET", "/live/rtc_fixture", "", true, true, false},
		{"route allowlist", "api.openai.com", "GET", "/live/rtc_fixture", "POST /embeddings", true, true, false},
		{"upgrade disabled", "api.openai.com", "GET", "/live/rtc_fixture", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := "https://" + tc.host
			target, _ := url.Parse(base + "/v1")
			origin, _ := Origin(base)
			route := activeRoute{Route: Route{WebSocket: tc.websocket}, target: target, origin: origin}
			if tc.allow != "" {
				route.Allow = []string{tc.allow}
			}
			req := httptest.NewRequest(tc.method, target.String()+tc.path, nil)
			if tc.upgrade {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			if got := accountRequestAllowed(account, req, route); got != tc.want {
				t.Errorf("account policy allowed = %t, want %t", got, tc.want)
			}
		})
	}
}
