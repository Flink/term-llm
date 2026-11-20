package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func TestPolicyLegacyAuthorityGoldens(t *testing.T) {
	// Independent pre-extension vectors from workspace LEGACY-AUTHORITY-GOLDENS.json.
	r := Route{Upstream: "https://api.example.test/base/"}
	u, _ := url.Parse(r.Upstream)
	origin, _ := Origin(r.Upstream)
	for _, tc := range []struct {
		rules map[string]HeaderRule
		want  string
	}{
		{nil, "6faec26371332d536ab61b227b8f9054aae640dd95dc0520755d75fc5a0fe843"},
		{map[string]HeaderRule{"Authorization": {Secret: "key", Prefix: "Bearer "}}, "4bc833f091bbb88057ca774fa6b31c3cc3962238203e907fd0e686aa6863988a"},
	} {
		r.SetHeaders = tc.rules
		if got := routeAuthority(r, origin, u); got != tc.want {
			t.Fatalf("legacy fingerprint %s want %s", got, tc.want)
		}
	}
	base := routeAuthority(r, origin, u)
	for _, mutate := range []func(*Route){func(r *Route) { r.Account = "account" }, func(r *Route) { r.WebSocket = true }, func(r *Route) { r.Allow = []string{"POST /responses"} }} {
		next := r
		mutate(&next)
		if routeAuthority(next, origin, u) == base {
			t.Fatal("scope change did not invalidate authority")
		}
	}
	r.Usage, r.Redirects = "auto", "refuse"
	if routeAuthority(r, origin, u) != base {
		t.Fatal("observation/redirect policy changed authority")
	}
}

func TestPolicyValidationAndMatching(t *testing.T) {
	for _, rule := range []string{"post /v1", "GET /a/../b", "GET /a//b", "GET /a%2fb", "GET /a?b", "GET  /a", "CONNECT /", "GET /a\\b", "GET /a#b", "GET relative"} {
		r := Route{Allow: []string{rule}}
		if validatePolicy(&r) == nil {
			t.Errorf("accepted %q", rule)
		}
	}
	for _, r := range []Route{{Account: "../account"}, {Redirects: "follow"}, {Usage: "on"}, {Allow: []string{"GET /", "GET /"}}} {
		if validatePolicy(&r) == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	r := Route{Allow: []string{"POST /responses", "GET /models/"}}
	if err := validatePolicy(&r); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/responses", true}, {"POST", "/responses/next", true}, {"GET", "/models/id", true},
		{"GET", "/responses", false}, {"POST", "/%72esponses", false}, {"POST", "/responses%252fadmin", false},
		{"POST", "/responses/../admin", false}, {"POST", "/responses//admin", false},
	} {
		if got := policyAllows(r, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s = %v", tc.method, tc.path, got)
		}
	}
	a := r
	a.Allow = []string{"GET /models/", "POST /responses"}
	_ = validatePolicy(&a)
	if strings.Join(r.Allow, ",") != strings.Join(a.Allow, ",") {
		t.Fatal("rule order not canonical")
	}
}

func TestPolicyCatchAllValidation(t *testing.T) {
	for _, entry := range []string{
		"* /*", "G*T /*", "get /*", "GET /v1*", "GET /v1/*/x", "GET /v1/*x", "GET /v1/*/*",
		"GET /v1///*", "GET /v1/%2f/*", "GET /v1/../*", "GET /v1?x/*",
	} {
		if err := validatePolicy(&Route{Allow: []string{entry}}); err == nil {
			t.Errorf("accepted invalid catch-all %q", entry)
		}
	}
	for _, r := range []Route{
		{Name: "fixture", Provider: "fixture", Account: "account", Allow: []string{"GET /*"}},
		{Name: "fixture", Provider: "fixture", Account: "account", Rules: []RouteRule{{PathPrefix: "/reader/", Allow: []string{"GET /*"}}}},
	} {
		c := Config{Routes: []Route{r}, Providers: map[string]Provider{"fixture": {Label: "Fixture"}}}
		if err := validateConfigStructure(c); err == nil || !strings.Contains(err.Error(), "catch-all allow rules are not permitted on account routes") {
			t.Errorf("catch-all account validation = %v", err)
		}
	}
}

func TestPolicyCatchAllMatchingAndAuthority(t *testing.T) {
	r := Route{Upstream: "https://example.test", Allow: []string{"GET /plain"}}
	u, err := url.Parse(r.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := Origin(r.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	before := routeAuthority(r, origin, u)
	r.Allow = append(r.Allow, "GET /v1/*")
	if err := validatePolicy(&r); err != nil {
		t.Fatal(err)
	}
	if routeAuthority(r, origin, u) == before {
		t.Fatal("adding a catch-all did not invalidate authority")
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/v1/", true}, {"GET", "/v1/a//b:id%2Fc", true}, {"POST", "/v1/x", false},
		{"GET", "/v1", false}, {"GET", "/v10/x", false}, {"GET", "/v1%2Fx", false},
		{"GET", "/v1/a/../x", false}, {"GET", "/v1/%25z/%252e%252e/x", false}, {"GET", "/v1/100%25/%255c/x", false},
		{"GET", "/plainfoo", true}, {"GET", "/plain/a%2Fb", false}, {"GET", "/plain//b", false},
	} {
		if got := policyAllows(r, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
	keenable, _ := proxyprofile.ByID("keenable")
	r.Allow = keenable.Allow
	if len(r.Allow) == 0 || !policyAllows(r, "POST", "/search/public") || policyAllows(r, "GET", "/search/public") {
		t.Fatal("Keenable's plain POST /search fence must include /search/public, but not GET")
	}
}

func TestPolicyBedrockChecksModelPathsBeforeDispatch(t *testing.T) {
	s := testService(t)
	a := accountRecord{AccountInfo: AccountInfo{Name: "account", Kind: "aws-sigv4"}}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	r := activeRoute{Route: Route{Account: a.Name, Allow: []string{"POST /model/"}}}
	if !s.routePolicyAllows(r, "POST", "/model/arn%3Aaws%3Abedrock%2Fprofile/converse") {
		t.Fatal("Bedrock's plain prefix must match an escaped model ID")
	}
	// Even bypassing config validation cannot evade Bedrock's operation fence.
	r.Allow = []string{"POST /*"}
	if s.routePolicyAllows(r, "POST", "/model/model-id/delete") || s.routePolicyAllows(r, "POST", "/other") {
		t.Fatal("catch-all bypassed the Bedrock model-path check")
	}
}

func TestPolicyCatchAllForwardsOpaqueTarget(t *testing.T) {
	const target = "/https://example.com/a%2Fb?x=1"
	for _, child := range []bool{false, true} {
		name := "route"
		if child {
			name = "child rule"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				credential := req.Header.Values("Authorization")
				if len(credential) != 1 || credential[0] != "Bearer reader-key" {
					t.Error("upstream did not receive only the Jina Reader credential")
				}
				if req.Method != "GET" || req.RequestURI != target {
					t.Errorf("Jina request changed: %s %s", req.Method, req.RequestURI)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer up.Close()
			s := testService(t)
			profile, _ := proxyprofile.ByID("jina-reader")
			if len(profile.Allow) == 0 {
				t.Fatal("Jina Reader profile has no Allow fence")
			}
			r := Route{
				Name: "jina-reader", Provider: "jina", Profile: profile.ID, LocalPrefix: "/jina-reader/", Enabled: true,
				AllowHTTP: true, Upstream: up.URL, Allow: profile.Allow,
				CallerAuth: Auth{Header: "Authorization", Prefix: "Bearer "},
				SetHeaders: map[string]HeaderRule{"Authorization": {Secret: "key", Prefix: "Bearer "}},
			}
			mount := strings.TrimSuffix(r.LocalPrefix, "/")
			if child {
				r.Rules = []RouteRule{{PathPrefix: "/reader/", Upstream: r.Upstream, CallerAuth: r.CallerAuth, SetHeaders: r.SetHeaders, Allow: r.Allow}}
				r.Upstream, r.CallerAuth, r.SetHeaders, r.Allow = "", Auth{}, nil, nil
				mount += "/reader"
			}
			configure(t, s, Config{Routes: []Route{r}, Providers: map[string]Provider{"jina": {Label: "Jina"}}}, map[string]material{"key": {Value: "reader-key"}})
			token := caller(t, s, r.Name)
			if w := rulesRequest(s, "GET", mount+target, Auth{Header: "Authorization", Prefix: "Bearer "}, token); w.Code != 204 {
				t.Fatalf("GET: %d %s", w.Code, w.Body.String())
			}
			if w := rulesRequest(s, "POST", mount+target, Auth{Header: "Authorization", Prefix: "Bearer "}, token); w.Code != 403 {
				t.Fatalf("POST: %d %s", w.Code, w.Body.String())
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("upstream received %d requests, want 1", n)
			}
		})
	}
}

func TestPolicyDeniedLedgerAndRedirectRefusal(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Subscription-Token") != "vendor-key" || len(r.Header.Values("X-Subscription-Token")) != 1 || r.Header.Get("Authorization") != "" {
			t.Error("upstream did not receive only the vendor credential")
		}
		w.Header().Set("Location", "https://private.invalid/secret")
		w.WriteHeader(307)
	}))
	defer up.Close()
	s := testService(t)
	profile, _ := proxyprofile.ByID("brave")
	r := Route{
		Name: "brave", Provider: "brave", Profile: profile.ID, LocalPrefix: "/local/", Upstream: up.URL, Enabled: true,
		AllowHTTP: true, Allow: profile.Allow, Redirects: "refuse", CallerAuth: Auth{Header: "X-Subscription-Token"},
		SetHeaders: map[string]HeaderRule{"X-Subscription-Token": {Secret: "key"}},
	}
	cfg := Config{Routes: []Route{r}, Providers: map[string]Provider{"brave": {Label: "Brave"}}}
	secrets := map[string]material{"key": {Value: "vendor-key"}}
	configure(t, s, cfg, secrets)
	token := caller(t, s, r.Name)
	denials := []struct{ method, path string }{
		{"GET", "web/suggest"}, {"GET", "web/%73earch"}, {"GET", "web/search%2fadmin"},
		{"GET", "web/search%252fadmin"}, {"POST", "web/search"},
	}
	for _, tc := range denials {
		rr := rulesRequest(s, tc.method, r.LocalPrefix+tc.path, r.CallerAuth, token)
		if rr.Code != 403 || rr.Header().Get("X-Term-LLM-Proxy-Request-ID") == "" {
			t.Fatalf("denial %s %s: %d", tc.method, tc.path, rr.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("denied request dispatched")
	}
	rr := rulesRequest(s, "GET", r.LocalPrefix+"web/searchfoo", r.CallerAuth, token)
	if rr.Code != 502 || rr.Header().Get("Location") != "" || strings.Contains(rr.Body.String(), "private.invalid") || rr.Header().Get("X-Term-LLM-Proxy-Redirect") != "refused" {
		t.Fatalf("redirect response: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
	}
	records, _, err := s.store.records(nil, 50)
	if err != nil || len(records) != len(denials)+1 {
		t.Fatalf("ledger %v %v", records, err)
	}
	denied := 0
	for _, rec := range records {
		if rec.Outcome == "denied" {
			denied++
		}
	}
	if denied != len(denials) {
		t.Fatalf("denied records = %d", denied)
	}
	cfg.Routes[0].Redirects = ""
	configure(t, s, cfg, secrets)
	rr = rulesRequest(s, "GET", r.LocalPrefix+"web/searchfoo", r.CallerAuth, token)
	if rr.Code != 307 || rr.Header().Get("Location") == "" {
		t.Fatal("default redirects changed")
	}
}
