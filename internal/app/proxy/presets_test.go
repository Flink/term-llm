package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// Presets project the native routing table except for the explicitly combined
// OpenCode Go preset. All other route identities, authentication, billing,
// transport and custody kinds remain faithful; gated profiles are not offered.
func TestRoutePresetsProjectProfiles(t *testing.T) {
	presets := map[string]routePreset{}
	for _, preset := range routePresets {
		if _, dup := presets[preset.ID]; dup || !slug.MatchString(preset.ID) || preset.Label == "" {
			t.Fatal("invalid or duplicate preset", preset.ID)
		}
		presets[preset.ID] = preset
	}
	for _, p := range proxyprofile.All() {
		preset, ok := presets[p.ID]
		if p.Gate != "" || p.Account != "" && !accountKind(p.Account) || p.ID == "opencode-go-messages" || p.ID == "opencode-go-gemini" {
			if ok {
				t.Errorf("%s is gated or has no custody support but is offered as a preset", p.ID)
			}
			continue
		}
		if !ok {
			t.Errorf("%s has no preset", p.ID)
			continue
		}
		delete(presets, p.ID)
		if p.ID == "opencode-go" {
			continue // Explicit composite preset; checked in TestOpenCodeGoPresetRules.
		}
		if preset.Label != p.Label || preset.Upstream != p.Upstream || preset.Billing != p.Billing || preset.WebSocket != p.WebSocket || preset.AccountKind != p.Account {
			t.Errorf("%s diverges from its profile: %+v", p.ID, preset)
		}
		switch preset.Billing {
		case "pay_as_you_go", "plan", "local", "unspecified":
		default:
			t.Errorf("%s has invalid billing %q", p.ID, preset.Billing)
		}
		if preset.Usage != "" && preset.Usage != "auto" {
			t.Errorf("%s has invalid usage %q", p.ID, preset.Usage)
		}
		if p.Provider != "" && preset.Usage != "auto" {
			t.Errorf("%s is a model provider without usage observation", p.ID)
		}
		if p.Billing == "local" {
			if preset.Auth != (Auth{}) {
				t.Errorf("%s substitutes a key for a local server", p.ID)
			}
		} else if preset.Auth != (Auth{Header: p.Header, Prefix: p.Prefix}) {
			t.Errorf("%s client authentication diverges: %+v", p.ID, preset.Auth)
		}
		if p.WebSocket != strings.Contains(preset.Note, "WebSocket") || strings.Contains(strings.ToLower(preset.Note), "not websocket") {
			t.Errorf("%s note misstates WebSocket support: %q", p.ID, preset.Note)
		}
		if p.PathSuffix != "" && !strings.Contains(preset.Note, p.PathSuffix) {
			t.Errorf("%s note omits its endpoint suffix", p.ID)
		}
		if p.Account == "claude-code" && (!strings.Contains(preset.Note, "Set up Claude") || strings.Contains(preset.Note, "choose it below")) {
			t.Errorf("Claude must use its simple setup, not account plumbing: %q", preset.Note)
		}
		if p.Account == "agy-bin" && (!strings.Contains(preset.Note, "Personal subscriptions only") || !strings.Contains(preset.Note, "proxy run agy -- acp --login") || strings.Contains(preset.Note, "choose it below")) {
			t.Errorf("Antigravity must use its personal native setup, not account plumbing: %q", preset.Note)
		}
		if p.Account != "" && p.Account != "claude-code" && p.Account != "agy-bin" && !strings.Contains(preset.Note, "trusted "+p.Account+" account") {
			t.Errorf("%s note omits its account requirement", p.ID)
		}
	}
	if len(presets) != 0 {
		t.Errorf("presets without a profile: %v", presets)
	}
	known := map[string]bool{}
	for _, p := range proxyprofile.All() {
		known[p.ID] = true
	}
	for id := range presetNotes {
		if !known[id] {
			t.Errorf("note for unknown profile %s", id)
		}
	}
	// Regression: these were wrongly documented as unsupported.
	for _, id := range []string{"opencode-go-gemini", "ollama", "openai"} {
		for _, stale := range []string{"cannot", "Not the dynamic", "not WebSocket"} {
			if note := presetNotes[id]; strings.Contains(note, stale) {
				t.Errorf("%s keeps a stale limitation: %q", id, note)
			}
		}
	}
}

func presetRoute(preset routePreset, upstream string) Route {
	if len(preset.Rules) != 0 {
		r := Route{Name: preset.ID, LocalPrefix: "/" + preset.ID + "/", Enabled: true, Billing: preset.Billing, Usage: preset.Usage, WebSocket: preset.WebSocket}
		for _, rule := range preset.Rules {
			r.Rules = append(r.Rules, RouteRule{PathPrefix: rule.PathPrefix, Upstream: rule.Upstream, CallerAuth: rule.Auth, SetHeaders: map[string]HeaderRule{rule.Auth.Header: {Secret: "key", Prefix: rule.Auth.Prefix}}})
		}
		return r
	}
	auth := preset.Auth
	if auth.Header == "" {
		auth = Auth{Header: "Authorization", Prefix: "Bearer "}
	}
	headers := map[string]HeaderRule{}
	if preset.AccountKind == "" && preset.Auth.Header != "" {
		headers[preset.Auth.Header] = HeaderRule{Secret: "key", Prefix: preset.Auth.Prefix}
	}
	return Route{Name: preset.ID, LocalPrefix: "/" + preset.ID + "/", Upstream: upstream, Enabled: true, AllowHTTP: strings.HasPrefix(upstream, "http:"), CallerAuth: auth, SetHeaders: headers, Billing: preset.Billing, Usage: preset.Usage, WebSocket: preset.WebSocket}
}

func TestRoutePresetsCompileAndForwardNativeCredentials(t *testing.T) {
	for _, preset := range routePresets {
		t.Run(preset.ID, func(t *testing.T) {
			if preset.Upstream == "" {
				return // Template: the operator supplies the server URL.
			}
			target, err := url.Parse(preset.Upstream)
			if err != nil {
				t.Fatal(err)
			}
			if target.Scheme != "https" && preset.Billing != "local" {
				t.Fatal("unexpected insecure preset")
			}
			r := presetRoute(preset, preset.Upstream)
			materials := map[string]material{}
			if len(r.SetHeaders) > 0 {
				materials["key"] = boundMaterial(t, preset.Upstream, "upstream-test-key")
			}
			if preset.AccountKind != "" {
				// Custody routes carry no static secret; the account integration is
				// exercised by the account tests, so only the route shape is checked.
				r.Account = "account"
				if _, err := compile(Config{Routes: []Route{r}}, 1, materials); err != nil || len(r.SetHeaders) != 0 {
					t.Fatalf("account preset does not compile without a static secret: %v", err)
				}
				return
			}
			if _, err := compile(Config{Routes: []Route{r}}, 1, materials); err != nil {
				t.Fatal(err)
			}
			const suffix = "chat/completions"
			var requests atomic.Int32
			token := ""
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				if req.URL.Path != strings.TrimSuffix(target.Path, "/")+"/"+suffix || req.URL.RawQuery != "alt=sse" {
					t.Errorf("wrong upstream URL: %s", req.URL)
				}
				for _, values := range req.Header {
					for _, value := range values {
						if strings.Contains(value, token) {
							t.Error("caller token forwarded upstream")
						}
					}
				}
				if preset.Auth.Header != "" && req.Header.Get(preset.Auth.Header) != preset.Auth.Prefix+"upstream-test-key" {
					t.Error("wrong upstream auth")
				}
				if preset.Auth.Header != "Authorization" && req.Header.Get("Authorization") != "" {
					t.Error("unexpected Authorization")
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != `{"model":"test"}` {
					t.Error("request body changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {}\n\n")
			}))
			defer up.Close()
			s := testService(t)
			r = presetRoute(preset, up.URL+target.Path)
			if len(r.SetHeaders) > 0 {
				materials["key"] = boundMaterial(t, r.Upstream, "upstream-test-key")
			}
			configure(t, s, Config{Routes: []Route{r}}, materials)
			if requests.Load() != 0 {
				t.Fatal("configuration probed upstream")
			}
			token = caller(t, s, r.Name)
			req := httptest.NewRequest(http.MethodPost, r.LocalPrefix+suffix+"?alt=sse", strings.NewReader(`{"model":"test"}`))
			req.Header.Set(r.CallerAuth.Header, r.CallerAuth.Prefix+token)
			result := httptest.NewRecorder()
			s.ServeHTTP(result, req)
			if result.Code != 200 || result.Body.String() != "data: {}\n\n" || requests.Load() != 1 {
				t.Fatalf("forwarding failed: %d %s", result.Code, result.Body.String())
			}
		})
	}
}

func TestRoutePresetsRequireAuthenticatedState(t *testing.T) {
	s := testService(t)
	if result := adminRequest(s, "GET", "state", nil, nil, ""); result.Code != http.StatusUnauthorized {
		t.Fatal(result.Code)
	}
	cookie, csrf := loginAdmin(t, s)
	result := adminRequest(s, "GET", "state", nil, cookie, csrf)
	var state struct {
		Presets []routePreset `json:"presets"`
	}
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &state) != nil || len(state.Presets) != len(routePresets) {
		t.Fatalf("missing presets: %d %s", result.Code, result.Body.String())
	}
}

// The picker offers one entry per provider; companions ride along with their
// parent and share its group so existing routes still label consistently.
func TestRoutePresetGroupsAndCompanions(t *testing.T) {
	byID := map[string]routePreset{}
	for _, p := range routePresets {
		byID[p.ID] = p
		switch p.Group {
		case "plan", "api_key", "self_hosted", "media":
		default:
			t.Errorf("%s has invalid group %q", p.ID, p.Group)
		}
	}
	want := map[string]string{"claude-code": "plan", "chatgpt": "plan", "opencode-go": "plan", "anthropic": "api_key", "bedrock": "api_key", "ollama": "self_hosted", "vllm": "self_hosted", "openai-compatible": "self_hosted", "elevenlabs": "media", "voyage": "media", "chatgpt-openai": "plan", "gemini-live": "api_key", "grok-bin": "plan"}
	for id, group := range want {
		if byID[id].Group != group {
			t.Errorf("%s group = %q, want %q", id, byID[id].Group, group)
		}
	}
	companions := map[string][]string{"chatgpt": {"chatgpt-backend", "chatgpt-openai"}, "gemini": {"gemini-live"}, "grok": {"grok-bin"}}
	for _, p := range routePresets {
		if got := strings.Join(p.Companions, ","); got != strings.Join(companions[p.ID], ",") {
			t.Errorf("%s companions = %q", p.ID, got)
		}
		for _, id := range p.Companions {
			c := byID[id]
			if c.AccountKind != p.AccountKind || len(c.Companions) != 0 {
				t.Errorf("%s companion %s must share custody and not nest", p.ID, id)
			}
		}
	}
}
