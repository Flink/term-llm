package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/term-llm/term-llm/internal/classify"
	"github.com/term-llm/term-llm/internal/classify/backends"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/providerhttp"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

var classifierIDs = []string{"typesafe", "openai-decisions", "cloudflare-clef"}

func TestClassificationProviderCreationAndState(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	create := func(name, preset, upstream, value, secret string, want int) {
		t.Helper()
		in := providerCreateInput{Revision: s.snap.revision, Name: name, Preset: preset, Value: value, Secret: secret}
		if upstream != "" {
			in.Routes = []providerRouteInput{{Name: name, Upstream: upstream}}
		}
		proxyResponse[map[string]any](t, adminRequest(s, "POST", "providers", in, cookie, csrf), want)
	}
	create("typesafe", "typesafe", "", "", "", 400)
	create("named-decisions", "openai-decisions", "", "vendor", "", 400)
	for _, root := range []string{
		"", "https://api.cloudflare.com", "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/run",
		"https://api.cloudflare.com/client/v4/accounts/abc/ai/run/@cf/cloudflare/clef",
		"https://gateway.ai.cloudflare.com/v1/abc/gateway",
	} {
		create("cloudflare-clef", "cloudflare-clef", root, "cf-vendor", "", 400)
	}
	create("openai", "openai", "", "shared-vendor", "", 201)
	create("openai-decisions", "openai-decisions", "", "", "openai", 201)
	create("typesafe", "typesafe", "", "ts-vendor", "", 201)
	create("cloudflare-clef", "cloudflare-clef", "https://gateway.ai.cloudflare.com/v1/abc/test/workers-ai", "cf-vendor", "", 201)

	stateReply := adminRequest(s, "GET", "state", nil, cookie, csrf)
	state := proxyResponse[struct {
		Presets   []routePreset       `json:"presets"`
		Providers []adminProviderInfo `json:"providers"`
		Routes    []adminRouteInfo    `json:"routes"`
	}](t, stateReply, 200)
	for _, id := range classifierIDs {
		p, _ := proxyprofile.ByID(id)
		pi := slices.IndexFunc(state.Providers, func(info adminProviderInfo) bool { return info.ID == id })
		ri := slices.IndexFunc(state.Routes, func(info adminRouteInfo) bool { return info.Name == id })
		ci := slices.IndexFunc(state.Presets, func(preset routePreset) bool { return preset.ID == id })
		if pi < 0 || ri < 0 || ci < 0 {
			t.Fatalf("missing %s from admin state", id)
		}
		provider, route, preset := state.Providers[pi], state.Routes[ri], state.Presets[ci]
		if provider.Family != "classification" || provider.Status != "green" || !slices.Equal(provider.Routes, []string{id}) {
			t.Errorf("wrong provider state: %+v", provider)
		}
		root := "classify.providers." + p.ClassifyProvider
		if route.Profile != id || !slices.Equal(route.Serves, []string{root + ".api_key", root + ".base_url", "classify: " + p.ClassifyProvider}) || route.Usage != "off" || route.Billing != "unspecified" {
			t.Errorf("wrong route state: %+v", route)
		}
		if preset.Family != "classification" || !slices.Equal(preset.Allow, p.Allow) || preset.Upstream != p.Upstream || preset.Usage != "off" {
			t.Errorf("wrong preset state: %+v", preset)
		}
	}
	if !slices.Equal(routeSecretReferences(s.snap.config.Routes)["openai"], []string{"openai", "openai-decisions"}) {
		t.Fatal("chat and Decisions should be separately routed while sharing the stored key")
	}
	if strings.Contains(stateReply.Body.String(), "-vendor") {
		t.Fatal("admin state exposed a key value")
	}
}

func TestClassificationProviderShape(t *testing.T) {
	p, _ := routePresetByID("typesafe")
	for _, tc := range []struct {
		name  string
		edit  func(*Config)
		valid bool
	}{
		{"canonical", func(*Config) {}, true},
		{"legacy custom inference", func(c *Config) { c.Routes[0].Profile = "" }, true},
		{"classification without profile", func(c *Config) {
			c.Routes[0].Profile = ""
			c.Providers["typesafe"] = Provider{Family: "classification"}
		}, false},
		{"multiple routes", func(c *Config) {
			r := c.Routes[0]
			r.Name = "alternate"
			c.Routes = append(c.Routes, r)
		}, false},
		{"unprofiled companion", func(c *Config) {
			c.Routes = append(c.Routes, Route{Name: "custom", Provider: "typesafe"})
		}, false},
		{"websocket", func(c *Config) { c.Routes[0].WebSocket = true }, false},
		{"subpath rule", func(c *Config) { c.Routes[0].Rules = []RouteRule{{PathPrefix: "/"}} }, false},
		{"client hints", func(c *Config) { c.Providers["typesafe"] = Provider{Client: &proxyprofile.ClientProviderConfig{}} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Providers: map[string]Provider{"typesafe": {}}, Routes: []Route{p.newRoute("typesafe", "classifier-route", "")}}
			tc.edit(&c)
			if err := validateConfigStructure(c); (err == nil) != tc.valid {
				t.Fatalf("valid = %v, got %v", tc.valid, err)
			}
		})
	}
}

func TestClassificationDiscoveryNegotiatesWithoutChangingGrants(t *testing.T) {
	s := testService(t)
	var cfg Config
	for _, id := range append([]string{"openai", "exa"}, classifierIDs...) {
		p, _ := routePresetByID(id)
		r := p.newRoute(id, id, "")
		if id == "cloudflare-clef" {
			r.Upstream, r.Enabled = "https://api.cloudflare.com/client/v4/accounts/abc/ai/run/", false
		}
		cfg.Routes = append(cfg.Routes, r)
	}
	cfg.Defaults = map[string]string{"search": "exa", "classify": "typesafe"}
	configure(t, s, cfg, nil)

	client, token, err := s.store.createCaller("mixed client", []string{"openai", "exa", "typesafe"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a disabled route that was granted while enabled.
	if err := s.store.changeProviderGrants(t.Context(), client.ID, map[string]string{"cloudflare-clef": s.snap.routes[4].authority}); err != nil {
		t.Fatal(err)
	}
	query := func(token string, capabilities []string, wantStatus int, wantIDs []string, wantClassifyDefault bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/_proxy/client/v2/providers", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		for _, capability := range capabilities {
			r.Header.Add("X-Term-LLM-Proxy-Capabilities", capability)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != wantStatus || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "X-Term-LLM-Proxy-Capabilities" {
			t.Fatalf("unexpected discovery response: %d %v %s", w.Code, w.Header(), w.Body.String())
		}
		if wantStatus != http.StatusOK {
			return
		}
		out := proxyResponse[struct {
			Providers []clientProviderInfo             `json:"providers"`
			Defaults  map[string]clientProviderDefault `json:"defaults"`
		}](t, w, wantStatus)
		var ids []string
		for _, p := range out.Providers {
			ids = append(ids, p.ID)
			if p.ID == "typesafe" && (p.Family != "classification" || p.Config != nil || len(p.Routes) != 1 || p.Routes[0] != (clientProviderRoute{"typesafe", "/typesafe/", "typesafe", false})) {
				t.Errorf("bad classifier projection: %+v", p)
			}
		}
		wantDefaults := map[string]clientProviderDefault{"search": {"exa", "exa"}}
		if wantClassifyDefault {
			wantDefaults["classify"] = clientProviderDefault{"typesafe", "typesafe"}
		}
		if !slices.Equal(ids, wantIDs) || !maps.Equal(out.Defaults, wantDefaults) {
			t.Fatalf("wrong discovery for %q: %+v", capabilities, out)
		}
	}
	legacy := []string{"exa", "openai"}
	optedIn := []string{"exa", "openai", "typesafe"}
	query(token, nil, 200, legacy, false)
	query(token, []string{"classification-v2, unclassification"}, 200, legacy, false)
	query(token, []string{"future", " other, classification "}, 200, optedIn, false)
	query(token, []string{"classification, classification-default"}, 200, optedIn, true)
	query("wrong-token", []string{"classification"}, 401, nil, false)
	if err := s.store.changeProviderGrants(t.Context(), client.ID, map[string]string{"typesafe": ""}); err != nil {
		t.Fatal(err)
	}
	query(token, []string{"classification, classification-default"}, 200, legacy, false)

	// An unprofiled custom inference provider keeps its old family and is not
	// hidden simply because its name now also appears in the preset catalog.
	for i := range cfg.Routes {
		if cfg.Routes[i].Name == "openai-decisions" {
			cfg.Routes[i].Profile = ""
		}
	}
	configure(t, s, cfg, nil)
	custom := caller(t, s, "exa", "openai-decisions")
	for _, capabilities := range [][]string{nil, {"classification"}} {
		query(custom, capabilities, 200, []string{"exa", "openai-decisions"}, false)
	}
}

func TestClassificationBackendsThroughProxy(t *testing.T) {
	for _, tc := range []struct {
		id, base string
		models   []string
		paths    []string
	}{
		{"typesafe", "", []string{"jev-latest"}, []string{"/v1/systemone"}},
		{"openai-decisions", "/v1", []string{"gpt-6-luna"}, []string{"/decisions"}},
		{"cloudflare-clef", "/client/v4/accounts/abc/ai/run", []string{"clef", "clef-flash"}, []string{"/@cf/cloudflare/clef", "/@cf/cloudflare/clef-flash"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			s := testService(t)
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				serveClassificationFixture(t, w, r, tc.id, tc.base, tc.models, tc.paths)
			}))
			defer up.Close()
			var cfg Config
			if err := addPresetProvider(&cfg, providerCreateInput{Name: tc.id, Preset: tc.id, Secret: "key", Routes: []providerRouteInput{{Name: tc.id, Upstream: up.URL + tc.base, AllowHTTP: true}}}); err != nil {
				t.Fatal(err)
			}
			cfg.Defaults = map[string]string{"classify": tc.id}
			configure(t, s, cfg, map[string]material{"key": {Value: "vendor-" + tc.id}})
			server := httptest.NewServer(s)
			defer server.Close()
			r := cfg.Routes[0]
			client, token, err := s.store.createCaller("classifier", []string{r.Name}, s.snap)
			if err != nil {
				t.Fatal(err)
			}
			backend := loadManagedClassifier(t, tc.id, server.URL, token, up.URL+"/wrong-direct-root")
			models, err := backend.ListModels(t.Context())
			if err != nil || len(models.Models) == 0 || models.Models[0].Name != tc.models[0] {
				t.Fatalf("ListModels: %+v, %v", models, err)
			}
			wantCalls := int32(len(tc.models))
			if tc.id == "typesafe" {
				wantCalls++ // The other backends list models locally.
			}
			req := classify.Request{State: json.RawMessage(`"The light is green"`), Questions: map[string]classify.Question{
				"q": {Type: "noul", Instructions: json.RawMessage(`"Is the light green?"`)},
			}}
			if tc.id == "typesafe" {
				req.Images = []string{"data:image/png;base64,iVBO"}
			}
			for i, model := range tc.models {
				req.Model = model
				resp, err := backend.Classify(t.Context(), req)
				if err != nil || resp.Answers["q"].Noul == nil || *resp.Answers["q"].Noul != 0.9 {
					t.Fatalf("Classify %s: %+v, %v", model, resp, err)
				}
				p := strings.TrimPrefix(tc.paths[i], "/")
				for _, denial := range [][2]string{{"GET", p}, {"POST", p + "-extra"}, {"POST", p + "/"}, {"POST", p + "%2Fextra"}} {
					if got := rulesRequest(s, denial[0], r.LocalPrefix+denial[1], r.CallerAuth, token); got.Code != http.StatusForbidden {
						t.Errorf("allowed %s %s: %d %s", denial[0], denial[1], got.Code, got.Body.String())
					}
				}
			}
			if err := s.store.changeProviderGrants(t.Context(), client.ID, map[string]string{r.Name: ""}); err != nil {
				t.Fatal(err)
			}
			_, err = backend.Classify(t.Context(), req)
			var status *providerhttp.StatusError
			if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized {
				t.Errorf("revoked grant should return 401, got %v", err)
			}
			if calls.Load() != wantCalls {
				t.Errorf("upstream calls = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func loadManagedClassifier(t *testing.T, id, proxyURL, token, directURL string) classify.Backend {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "term-llm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p, _ := proxyprofile.ByID(id)
	// The local endpoint points at our own fixture so even a regression cannot
	// contact a real vendor. Successful calls must use the discovered route.
	body := fmt.Sprintf(`proxy:
  url: %s
  token: %s
classify:
  providers:
    %s:
      base_url: %s
      api_key: unused-direct-key
`, proxyURL, token, p.ClassifyProvider, directURL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutCredentials()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := cfg.Classify.ResolveProvider("")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := backends.Open(provider, backends.Overrides{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func serveClassificationFixture(t *testing.T, w http.ResponseWriter, r *http.Request, id, base string, models, paths []string) {
	t.Helper()
	if got := r.Header.Values("Authorization"); !slices.Equal(got, []string{"Bearer vendor-" + id}) {
		t.Error("upstream did not receive only its vendor credential")
	}
	w.Header().Set("Content-Type", "application/json")
	if id == "typesafe" && r.Method == http.MethodGet && r.RequestURI == "/v1/models" {
		io.WriteString(w, `{"models":[{"name":"jev-latest"}]}`)
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("bad request body: %v", err)
	}
	i := slices.Index(models, body.Model)
	if i < 0 || r.Method != http.MethodPost || r.RequestURI != base+paths[i] {
		t.Errorf("unexpected %s %s for model %q", r.Method, r.RequestURI, body.Model)
		http.NotFound(w, r)
		return
	}
	switch id {
	case "typesafe":
		if got := r.Header.Get("TypeSafe-Danger-Zone-Images"); got != "true" {
			t.Errorf("missing image opt-in header: %q", got)
		}
		io.WriteString(w, `{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.9}}}`)
	case "openai-decisions":
		io.WriteString(w, `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"q","probability":0.9}]}`)
	case "cloudflare-clef":
		io.WriteString(w, `{"result":{"model":"`+body.Model+`","answers":{"q":{"type":"noul","noul":0.9}}},"success":true}`)
	}
}
