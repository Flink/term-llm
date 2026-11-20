package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func claudeSetupAssertMetadata(t *testing.T, s *Service, out nativeSetupReply, route string) {
	t.Helper()
	nativeSetupAssertMetadata(t, s, out, route, claudeProxyProvider)
}

func nativeSetupAssertMetadata(t *testing.T, s *Service, out nativeSetupReply, route string, p *nativeProxyProvider) {
	t.Helper()
	// This is the exact authentication shape used by the native helper. The
	// handler generates local metadata/CA only; no vendor request is made.
	r := httptest.NewRequest("GET", out.URL+p.discovery, nil)
	// net/http's server receives origin-form URLs, not a forward-proxy target.
	r.URL.Scheme, r.URL.Host = "", ""
	r.Header.Set("Authorization", "Bearer "+out.Token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var meta struct {
		Route           string `json:"route"`
		Certificate     string `json:"certificate_pem"`
		TokenMode       string `json:"token_mode"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &meta); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || err != nil || meta.Route != route || meta.ProtocolVersion != 2 || meta.TokenMode != "vault" || meta.Certificate == "" {
		t.Fatalf("returned connection cannot fetch native metadata: %d %s err=%v", w.Code, w.Body.String(), err)
	}
}

func TestClaudeSetupIncompatibleRouteNotAdopted(t *testing.T) {
	for _, variant := range []string{"custom header", "custom prefix", "narrow allow", "disabled incompatible"} {
		t.Run(variant, func(t *testing.T) {
			s := testService(t)
			claudeSetupAccount(t, s, "existing-slot", "claude-bin")
			custom := claudeSetupRoute("custom", "existing-slot")
			switch variant {
			case "custom header":
				custom.CallerAuth.Header = "X-Local-Key"
			case "custom prefix":
				custom.CallerAuth.Prefix = "Custom "
			case "narrow allow":
				custom.Allow = []string{"GET /v1/models", "POST /v1/messages"}
			case "disabled incompatible":
				custom.Enabled = false
				custom.CallerAuth = Auth{Header: "X-Local-Key", Prefix: "Custom "}
			}
			configure(t, s, Config{Routes: []Route{custom}}, nil)
			before := claudeSetupDBState(t, s, "accounts")
			cookie, csrf := loginAdmin(t, s)
			for _, action := range []string{"", "connect"} {
				rr := adminRequest(s, "POST", "claude/setup", nativeSetupInput{Revision: &s.snap.revision, Route: custom.Name, SharedAction: action}, cookie, csrf)
				if rr.Code != 400 {
					t.Fatalf("adopted incompatible route: %d %s", rr.Code, rr.Body.String())
				}
			}
			if len(s.snap.routes) != 1 || claudeSetupDBState(t, s, "accounts") != before {
				t.Fatal("rejected setup changed existing routes or accounts")
			}
			out := claudeSetupPost(t, s, cookie, csrf)
			if len(s.snap.routes) != 2 || !reflect.DeepEqual(s.snap.config.Routes[0], custom) || s.snap.routes[1].Account == custom.Account || !claudeSetup.compatible(s.snap.config.Routes[1]) {
				t.Fatalf("custom binding overwritten or broken binding reused: %+v", s.snap.config.Routes)
			}
			if a, err := s.store.account(custom.Account); err != nil || !a.claudeShared() || a.State != "needs_login" {
				t.Fatalf("original shared account was modified: %+v %v", a.AccountInfo, err)
			}
			claudeSetupAssertMetadata(t, s, out, s.snap.routes[1].Name)
		})
	}
}
