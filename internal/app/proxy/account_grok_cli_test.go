package proxy

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGrokAccountAllowsNativeAndCLIInferenceButNotAdministration(t *testing.T) {
	target, _ := url.Parse("https://cli-chat-proxy.grok.com/v1")
	route := activeRoute{Route: Route{Account: "grok", Upstream: target.String()}, target: target}
	account := accountRecord{AccountInfo: AccountInfo{Kind: "grok"}}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/v1/responses", true},
		{"POST", "/v1/chat/completions", true},
		{"GET", "/v1/models", true},
		{"DELETE", "/v1/chat/completions", false},
		{"POST", "/v1/chat/completions-extra", false},
		{"POST", "/v1/api_keys", false},
		{"POST", "/v1/billing", false},
	} {
		req := httptest.NewRequest(tc.method, "https://cli-chat-proxy.grok.com"+tc.path, nil)
		if got := accountRequestAllowed(account, req, route); got != tc.want {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// A preset change is not a migration. Previously saved CLI routes still compile
// as part of Grok and are discoverable under their own identity and grant.
func TestLegacyGrokCLIGrantDoesNotExposePlanRoute(t *testing.T) {
	s := testService(t)
	plan := providerTestRoute(t, "grok", "shared")
	cli := plan
	cli.Name, cli.Profile, cli.LocalPrefix = "grok-bin", "grok-bin", "/grok-bin/"
	configure(t, s, Config{Routes: []Route{plan, cli}}, nil)
	req := httptest.NewRequest("GET", "/_proxy/client/v2/providers", nil)
	req.Header.Set("Authorization", "Bearer "+caller(t, s, cli.Name))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	out := proxyResponse[struct{ Providers []clientProviderInfo }](t, w, 200)
	if len(out.Providers) != 1 || out.Providers[0].ID != "grok" || len(out.Providers[0].Routes) != 1 || out.Providers[0].Routes[0].Profile != "grok-bin" {
		t.Fatalf("unexpected discovery for existing CLI-only grant: %+v", out.Providers)
	}
}
