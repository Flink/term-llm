package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func rulesRoute(t *testing.T, upstream string) Route {
	t.Helper()
	r := presetRoute(openCodeGoPreset(t), "")
	r.AllowHTTP = strings.HasPrefix(upstream, "http:")
	for i := range r.Rules {
		r.Rules[i].Upstream = strings.Replace(r.Rules[i].Upstream, "https://opencode.ai", upstream, 1)
	}
	return r
}

func rulesRequest(s *Service, method, path string, auth Auth, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(auth.Header, auth.Prefix+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestRouteRulesThreeProtocolsOneGrant(t *testing.T) {
	var calls atomic.Int32
	cases := []struct {
		local, upstream, body string
		protocol              usagetap.Protocol
		auth                  Auth
	}{
		{"chat/completions", "/zen/go/v1/chat/completions", `{"model":"chat-test","usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`, usagetap.ChatCompletions, Auth{Header: "Authorization", Prefix: "Bearer "}},
		{"messages/v1/messages", "/zen/go/v1/messages", `{"type":"message","model":"claude-test","usage":{"input_tokens":12,"output_tokens":7}}`, usagetap.Messages, Auth{Header: "x-api-key"}},
		{"gemini/models/test:generateContent", "/inference/go/v1/models/test:generateContent", `{"modelVersion":"gem-test","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19}}`, usagetap.Gemini, Auth{Header: "x-goog-api-key"}},
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		var selected int
		for i, tc := range cases {
			if req.URL.Path == tc.upstream {
				selected = i
				break
			}
		}
		tc := cases[selected]
		if req.URL.Path != tc.upstream || req.URL.RawQuery != "alt=sse&q=a%2Fb" {
			t.Errorf("wrong target: %s", req.URL)
		}
		if req.Header.Get(tc.auth.Header) != tc.auth.Prefix+"vendor-key" {
			t.Errorf("wrong substituted credential: %v", req.Header)
		}
		for _, other := range cases {
			if other.auth.Header != tc.auth.Header && req.Header.Get(other.auth.Header) != "" {
				t.Errorf("sibling credential leaked: %v", req.Header)
			}
		}
		if req.Header.Get("Cookie") != "" || req.Header.Get("X-Opencode-Client") != "native" || req.Header.Get("X-Opencode-Session") != "session" {
			t.Errorf("incorrect stripping: %v", req.Header)
		}
		_, _ = io.ReadAll(req.Body)
		for _, other := range cases {
			if req.Trailer.Get(other.auth.Header) != "" {
				t.Errorf("credential trailer leaked: %v", req.Trailer)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=secret")
		_, _ = io.WriteString(w, tc.body)
	}))
	defer up.Close()
	s := testService(t)
	r := rulesRoute(t, up.URL)
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "vendor-key")})
	token := caller(t, s, r.Name)
	clients, err := s.store.callers()
	if err != nil || len(clients) != 1 || !reflect.DeepEqual(clients[0].Grants, []string{r.Name}) {
		t.Fatalf("expected exactly one parent grant: %+v %v", clients, err)
	}
	for _, tc := range cases {
		req := httptest.NewRequest("POST", r.LocalPrefix+tc.local+"?alt=sse&q=a%2Fb", strings.NewReader(`{"model":"test"}`))
		for _, other := range cases {
			req.Header.Set(other.auth.Header, other.auth.Prefix+token)
		}
		req.Header.Set("Cookie", "local=secret")
		req.Header.Set("X-Opencode-Client", "native")
		req.Header.Set("X-Opencode-Session", "session")
		req.Trailer = http.Header{"X-Api-Key": {token}, "X-Goog-Api-Key": {token}}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != tc.body || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d %s", tc.local, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 || len(s.snap.config.Routes) != 1 || len(s.snap.routes) != 1 {
		t.Fatal("not one logical route / unexpected dispatches")
	}
	records, _, err := s.store.records(nil, 10)
	if err != nil || len(records) != 3 {
		t.Fatalf("ledger: %+v %v", records, err)
	}
	for _, rec := range records {
		if rec.Route != r.Name || rec.LocalPrefix != r.LocalPrefix || rec.Status != 200 || rec.Usage == nil || rec.Usage.Usage.Input != 12 || rec.Usage.Usage.Output != 7 {
			t.Errorf("accounting: %+v", rec)
		}
		found := false
		for _, tc := range cases {
			if rec.Path == "/"+tc.local && rec.Usage != nil && rec.Usage.Protocol == tc.protocol {
				found = true
			}
		}
		if !found {
			t.Errorf("lost protocol/subpath accounting: %+v", rec)
		}
	}
	req := httptest.NewRequest("GET", "/_proxy/client/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var identity struct {
		Routes []struct{ Name, LocalPrefix string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &identity); err != nil || w.Code != 200 || len(identity.Routes) != 1 || identity.Routes[0].Name != r.Name {
		t.Fatalf("whoami must show one parent: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}

func TestRouteRulesLongestBoundaryAndDenial(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, req.URL.Path)
	}))
	defer up.Close()
	s := testService(t)
	r := rulesRoute(t, up.URL)
	r.Rules[1].Allow = []string{"GET /allowed"}
	r.Rules = append(r.Rules, RouteRule{PathPrefix: "/messages/deep/", Upstream: up.URL + "/nested", CallerAuth: Auth{Header: "X-Deep"}, Allow: []string{"GET /ok"}})
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "vendor")})
	token := caller(t, s, r.Name)
	for _, tc := range []struct {
		path   string
		auth   Auth
		status int
		body   string
	}{
		{"messages/deep/ok", Auth{Header: "X-Deep"}, 200, "/nested/ok"},
		{"messages/deep/no", Auth{Header: "X-Deep"}, 403, ""},
		{"messages/deep/ok", r.Rules[1].CallerAuth, 401, ""},
		{"messages/allowed", r.Rules[1].CallerAuth, 200, "/zen/go/allowed"},
		{"messages/denied", r.Rules[1].CallerAuth, 403, ""},
		{"messages/allowed", r.Rules[0].CallerAuth, 401, ""},
		{"messages", r.Rules[1].CallerAuth, 403, ""},
		{"messages-extra/path", r.Rules[0].CallerAuth, 200, "/zen/go/v1/messages-extra/path"},
		{"messages/deeper/path", r.Rules[1].CallerAuth, 403, ""},
	} {
		w := rulesRequest(s, "GET", r.LocalPrefix+tc.path, tc.auth, token)
		if w.Code != tc.status || tc.body != "" && w.Body.String() != tc.body {
			t.Errorf("%s (%s): %d %s", tc.path, tc.auth.Header, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("denied requests fell through: %d dispatches", calls.Load())
	}
	// No implicit root rule is synthesized.
	r.Rules = r.Rules[1:]
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "vendor")})
	if w := rulesRequest(s, "GET", r.LocalPrefix+"unmatched", Auth{Header: "Authorization", Prefix: "Bearer "}, token); w.Code != 404 {
		t.Fatalf("unmatched rule: %d", w.Code)
	}
}

func TestRouteRulesValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Route)
	}{
		{"parent upstream", func(r *Route) { r.Upstream = "https://opencode.ai" }},
		{"parent headers", func(r *Route) { r.SetHeaders = map[string]HeaderRule{"X-Key": {Secret: "key"}} }},
		{"parent allow", func(r *Route) { r.Allow = []string{"GET /"} }},
		{"parent caller auth", func(r *Route) { r.CallerAuth = Auth{Header: "X-Key"} }},
		{"no slash", func(r *Route) { r.Rules[1].PathPrefix = "messages/" }},
		{"no trailing slash", func(r *Route) { r.Rules[1].PathPrefix = "/messages" }},
		{"duplicate", func(r *Route) { r.Rules[1].PathPrefix = "/" }},
		{"double slash", func(r *Route) { r.Rules[1].PathPrefix = "/messages//" }},
		{"escaped", func(r *Route) { r.Rules[1].PathPrefix = "/%6dessages/" }},
		{"traversal", func(r *Route) { r.Rules[1].PathPrefix = "/messages/../" }},
		{"invalid allow", func(r *Route) { r.Rules[1].Allow = []string{"GET /../"} }},
		{"missing secret", func(r *Route) { r.Rules[2].SetHeaders = map[string]HeaderRule{"X-Key": {Secret: "absent"}} }},
		{"unsafe header", func(r *Route) { r.Rules[2].CallerAuth.Header = "Cookie" }},
		{"unsafe upstream", func(r *Route) { r.Rules[2].Upstream = "https://opencode.ai/a/../" }},
		{"too many", func(r *Route) { r.Rules = make([]RouteRule, 33) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rulesRoute(t, "https://opencode.ai")
			tc.change(&r)
			if _, err := compile(fixtureProviders(Config{Routes: []Route{r}}), 1, map[string]material{"key": boundMaterial(t, "https://opencode.ai", "vendor")}); err == nil {
				t.Fatal("invalid rules accepted")
			}
		})
	}
}

func TestRouteRulesAuthorityAndGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Route)
	}{
		{"destination", func(r *Route) { r.Rules[2].Upstream += "/new-base" }},
		{"mapping", func(r *Route) { r.Rules[2].PathPrefix = "/new/" }},
		{"outbound auth", func(r *Route) { r.Rules[2].SetHeaders["x-goog-api-key"] = HeaderRule{Secret: "key", Prefix: "New "} }},
		{"secret reference", func(r *Route) { r.Rules[2].SetHeaders["x-goog-api-key"] = HeaderRule{Secret: "other"} }},
		{"allow", func(r *Route) { r.Rules[2].Allow = []string{"GET /models"} }},
		{"websocket", func(r *Route) { r.WebSocket = true }},
		{"redirect", func(r *Route) { r.Redirects = "refuse" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			r := rulesRoute(t, "https://opencode.ai")
			materials := map[string]material{"key": boundMaterial(t, "https://opencode.ai", "vendor"), "other": boundMaterial(t, "https://opencode.ai", "other")}
			configure(t, s, Config{Routes: []Route{r}}, materials)
			token := caller(t, s, r.Name)
			old := s.snap.routes[0].authority
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			s.sockets.Store("test", &activeSocket{route: r.Name, authority: old, cancel: cancel})
			tc.change(&r)
			configure(t, s, Config{Routes: []Route{r}}, materials)
			s.reconcileSockets(s.snap)
			if s.snap.routes[0].authority == old || context.Cause(ctx) != errSocketRevoked {
				t.Fatal("scope change retained authority or live stream")
			}
			for _, rule := range s.snap.routes[0].rules {
				if _, err := s.store.authorize(token, rule); err == nil {
					t.Fatal("another rule retained grant after scope change")
				}
			}
			token = caller(t, s, r.Name)
			for _, rule := range s.snap.routes[0].rules {
				if _, err := s.store.authorize(token, rule); err != nil {
					t.Fatal("one regrant did not authorize every rule", err)
				}
			}
		})
	}
}

func TestRouteRulesRotationAndLocalChangesKeepGrant(t *testing.T) {
	s := testService(t)
	r := rulesRoute(t, "https://opencode.ai")
	r.Rules[1].Allow = []string{"GET /models", "POST /messages"}
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, "https://opencode.ai", "old")})
	token := caller(t, s, r.Name)
	old := s.snap.routes[0].authority
	r.LocalPrefix = "/moved/"
	r.Rules[1].CallerAuth = Auth{Header: "X-New-Local", Prefix: "Local "}
	r.Rules[1].Allow = []string{"POST /messages", "GET /models"}
	r.Rules[0], r.Rules[2] = r.Rules[2], r.Rules[0]
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, "https://opencode.ai", "rotated")})
	if old != s.snap.routes[0].authority {
		t.Fatal("local or cosmetic changes invalidated scope")
	}
	for _, rule := range s.snap.routes[0].rules {
		if _, err := s.store.authorize(token, rule); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRouteRulesSecretDeletionAndLegacyPreservation(t *testing.T) {
	s := testService(t)
	r := rulesRoute(t, "https://opencode.ai")
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, "https://opencode.ai", "vendor")})
	if status, err := s.prepareSecretChange(secretInput{Name: "key"}, true, map[string]material{}); status != 409 || err == nil {
		t.Fatal("rule secret can be deleted while referenced")
	}
	for _, id := range []string{"opencode-go", "opencode-go-messages", "opencode-go-gemini"} {
		legacy := Route{Name: id, LocalPrefix: "/" + id + "/", Upstream: "https://opencode.ai/custom/unchanged", Enabled: true, CallerAuth: Auth{Header: "X-Custom", Prefix: "Native "}}
		fixture := fixtureProviders(Config{Routes: []Route{legacy}})
		snap, err := compile(fixture, 1, nil)
		if err != nil || len(snap.routes) != 1 || len(snap.routes[0].rules) != 0 || !reflect.DeepEqual(snap.config.Routes[0], fixture.Routes[0]) {
			t.Fatalf("legacy %s reinterpreted: %+v %v", id, snap, err)
		}
	}
}

func TestRouteRulesConfigRoundTrip(t *testing.T) {
	r := rulesRoute(t, "https://opencode.ai")
	data, err := json.Marshal(Config{Routes: []Route{r}})
	if err != nil {
		t.Fatal(err)
	}
	// JSON is valid YAML; exercise strict operator decoding as well as state JSON.
	config, err := decodeConfig(data)
	if err != nil || !reflect.DeepEqual(config.Routes[0], r) {
		t.Fatalf("rules schema roundtrip: %s: %v", data, err)
	}
	if _, err := compile(fixtureProviders(config), 1, map[string]material{"key": boundMaterial(t, "https://opencode.ai", "vendor")}); err != nil {
		t.Fatal(err)
	}
}
