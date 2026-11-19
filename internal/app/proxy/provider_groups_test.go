package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func providerTestRoute(t *testing.T, id, ref string) Route {
	t.Helper()
	preset, ok := routePresetByID(id)
	if !ok {
		t.Fatalf("no preset %q", id)
	}
	if preset.AccountKind != "" {
		return chatGPTPresetRoute(preset, id, ref)
	}
	route := presetRoute(preset, preset.Upstream)
	for header, rule := range route.SetHeaders {
		rule.Secret = ref
		route.SetHeaders[header] = rule
	}
	return route
}

func TestProviderGroupsState(t *testing.T) {
	s := testService(t)
	const fakeToken = "synthetic-provider-group-token"
	for _, account := range []AccountInfo{{Name: "work", Kind: "chatgpt"}, {Name: "grok", Kind: "grok"}} {
		record := accountRecord{AccountInfo: account, credentials: accountCredentials{AccessToken: fakeToken}}
		if err := s.store.replaceAccount(context.Background(), record, 0); err != nil {
			t.Fatal(err)
		}
	}
	main := providerTestRoute(t, "chatgpt", "work")
	main.Name, main.LocalPrefix, main.Enabled = "chat-at-work", "/work/codex/", false
	preset, _ := routePresetByID("chatgpt")
	companions := chatGPTCompanionRoutes(preset, main.Account, []Route{main})
	gemini := providerTestRoute(t, "gemini", "google-key")
	live := providerTestRoute(t, "gemini-live", "google-key")
	live.Enabled = false
	live.Upstream += "/"
	openai := providerTestRoute(t, "openai", "openai-key")
	grok := providerTestRoute(t, "grok", "grok")
	grokCLI := providerTestRoute(t, "grok-bin", "grok")
	routes := []Route{companions[0], gemini, openai, main, grokCLI, live, companions[1], grok}
	configure(t, s, Config{Routes: routes}, map[string]material{
		"google-key": boundMaterial(t, gemini.Upstream, fakeToken),
		"openai-key": boundMaterial(t, openai.Upstream, fakeToken),
	})
	if err := s.validateAccounts(s.snap); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAdmin(t, s)
	result := adminRequest(s, http.MethodGet, "state", nil, cookie, csrf)
	var state struct {
		Providers json.RawMessage `json:"providers"`
		Routes    []Route         `json:"routes"`
	}
	if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &state) != nil {
		t.Fatalf("GET state: %d %s", result.Code, result.Body.String())
	}
	const want = `[{"id":"chatgpt-backend","label":"ChatGPT","routes":["chatgpt-backend","chat-at-work","chatgpt-openai"]},{"id":"gemini","label":"Google Gemini","routes":["gemini","gemini-live"]},{"id":"openai","label":"OpenAI API","routes":["openai"]},{"id":"grok-bin","label":"Grok plan","routes":["grok-bin","grok"]}]`
	if string(state.Providers) != want {
		t.Errorf("providers = %s, want %s", state.Providers, want)
	}
	if !slices.EqualFunc(state.Routes, routes, func(got, want Route) bool {
		return got.Name == want.Name && got.LocalPrefix == want.LocalPrefix && got.Enabled == want.Enabled
	}) {
		t.Errorf("state routes changed: %+v", state.Routes)
	}
	if strings.Contains(result.Body.String(), fakeToken) {
		t.Fatal("state leaked a credential")
	}
}

func TestProviderGroupsDoNotInferUnrelatedCompanions(t *testing.T) {
	accounts := []AccountInfo{{Name: "work", Kind: "chatgpt"}, {Name: "personal", Kind: "chatgpt"}, {Name: "grok", Kind: "grok"}, {Name: "grok-other", Kind: "grok"}}
	chatGPT := providerTestRoute(t, "chatgpt", "work")
	backend := providerTestRoute(t, "chatgpt-backend", "personal")
	wrongEndpoint := providerTestRoute(t, "chatgpt-openai", "work")
	wrongEndpoint.Upstream += "/other" // Correct name, account and host are not enough.
	wrongRules := providerTestRoute(t, "chatgpt-backend", "work")
	wrongRules.Rules = []RouteRule{{PathPrefix: "/", Upstream: wrongRules.Upstream}, {PathPrefix: "/other/", Upstream: chatGPT.Upstream}}
	wrongRules.Upstream, wrongRules.CallerAuth = "", Auth{}
	wrongKind := providerTestRoute(t, "chatgpt-openai", "grok")
	apiAsCompanion := providerTestRoute(t, "openai", "work")
	apiAsCompanion.Name = "chatgpt-openai"
	unknownMain := providerTestRoute(t, "chatgpt", "missing")
	unknownCompanion := providerTestRoute(t, "chatgpt-openai", "missing")
	gemini := providerTestRoute(t, "gemini", "google-key")
	differentSecret := providerTestRoute(t, "gemini-live", "other-key")
	wrongHeader := providerTestRoute(t, "gemini-live", "google-key")
	wrongHeader.SetHeaders = map[string]HeaderRule{"Authorization": {Secret: "google-key", Prefix: "Bearer "}}
	literal := providerTestRoute(t, "gemini-live", "")
	literal.SetHeaders = map[string]HeaderRule{"x-goog-api-key": {Value: new("literal")}}
	grok := providerTestRoute(t, "grok", "grok")
	grokCLI := providerTestRoute(t, "grok-bin", "grok-other")
	customGrok := grok
	customGrok.Name, customGrok.LocalPrefix = "another-grok-mount", "/another-grok-mount/"
	sameAccountCLI := providerTestRoute(t, "grok-bin", grok.Account)
	xai := providerTestRoute(t, "xai", "grok")
	for _, tc := range []struct {
		name   string
		routes []Route
	}{
		{"different ChatGPT accounts", []Route{chatGPT, backend}},
		{"wrong upstream path", []Route{chatGPT, wrongEndpoint}},
		{"wrong rules", []Route{chatGPT, wrongRules}},
		{"wrong account kind", []Route{chatGPT, wrongKind}},
		{"unknown account", []Route{unknownMain, unknownCompanion}},
		{"OpenAI API with companion name", []Route{chatGPT, apiAsCompanion}},
		{"different Gemini secret references", []Route{gemini, differentSecret}},
		{"wrong Gemini credential header", []Route{gemini, wrongHeader}},
		{"literal Gemini substitution", []Route{gemini, literal}},
		{"different Grok accounts", []Route{grok, grokCLI}},
		{"xAI API is not the Grok plan", []Route{grok, xai}},
		{"duplicate Grok role is ambiguous", []Route{grok, sameAccountCLI, customGrok}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertProviderSingletons(t, providerGroups(tc.routes, accounts), tc.routes)
		})
	}
}

func TestProviderGroupSingletonLabels(t *testing.T) {
	claude := providerTestRoute(t, "claude-code", "native")
	claude.Name, claude.Upstream = "legacy-claude", claude.Upstream+"/legacy"
	rules := providerTestRoute(t, "opencode-go", "key")
	slices.Reverse(rules.Rules)
	custom := Route{Name: "gemini", Upstream: "https://example.com/v1beta"}
	routes := []Route{claude, rules, custom}
	groups := providerGroups(routes, []AccountInfo{{Name: "native", Kind: "claude-code"}})
	assertProviderSingletons(t, groups, routes)
	if want := []string{"Claude Code plan", "OpenCode Go", "gemini"}; !slices.EqualFunc(groups, want, func(group providerGroup, label string) bool {
		return group.Label == label
	}) {
		t.Errorf("singleton labels: %+v, want %v", groups, want)
	}
}

func TestProviderGroupsFileState(t *testing.T) {
	dir := t.TempDir()
	accountFile := filepath.Join(dir, "account.json")
	adminFile := filepath.Join(dir, "admin")
	configFile := filepath.Join(dir, "proxy.yaml")
	const boot = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"synthetic-id","expires_at":2000000000}`
	main := providerTestRoute(t, "chatgpt", "chatgpt")
	backend := providerTestRoute(t, "chatgpt-backend", main.Account)
	live := providerTestRoute(t, "chatgpt-openai", main.Account)
	custom := Route{Name: "custom", LocalPrefix: "/custom/", Upstream: "https://chatgpt.com/backend-api/custom", Account: main.Account, Enabled: true}
	config, err := yaml.Marshal(Config{
		Accounts: map[string]AccountRef{main.Account: {Kind: "chatgpt", File: accountFile}},
		Routes:   []Route{main, backend, live, custom},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{accountFile: boot, adminFile: strings.Repeat("a", 64), configFile: string(config)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(Options{Auth: "token", StateDir: filepath.Join(dir, "state"), AdminTokenFile: adminFile, ConfigFile: configFile})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cookie, csrf := loginAdmin(t, s)
	result := adminRequest(s, http.MethodGet, "state", nil, cookie, csrf)
	var state struct {
		Mode      string          `json:"mode"`
		Providers json.RawMessage `json:"providers"`
		Routes    []Route         `json:"routes"`
		Revision  int64           `json:"revision"`
	}
	if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &state) != nil || state.Mode != "file" || len(state.Routes) != 4 {
		t.Fatalf("GET file state: %d %s", result.Code, result.Body.String())
	}
	const want = `[{"id":"chatgpt","label":"ChatGPT","routes":["chatgpt","chatgpt-backend","chatgpt-openai"]},{"id":"custom","label":"custom","routes":["custom"]}]`
	if string(state.Providers) != want {
		t.Errorf("file groups = %s, want %s", state.Providers, want)
	}
	state.Routes[0].Provider = "chatgpt"
	result = adminRequest(s, http.MethodPut, "config", map[string]any{"revision": state.Revision, "routes": state.Routes}, cookie, csrf)
	if result.Code != http.StatusConflict {
		t.Errorf("file state became editable: %d %s", result.Code, result.Body.String())
	}
}

func TestProviderGroupsEmptyState(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	result := adminRequest(s, http.MethodGet, "state", nil, cookie, csrf)
	var state struct {
		Providers json.RawMessage `json:"providers"`
	}
	if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &state) != nil || string(state.Providers) != "[]" {
		t.Fatalf("GET empty state: %d %s", result.Code, result.Body.String())
	}
}

func assertProviderSingletons(t *testing.T, groups []providerGroup, routes []Route) {
	t.Helper()
	if len(groups) != len(routes) {
		t.Fatalf("want %d singleton groups, got %+v", len(routes), groups)
	}
	for i, route := range routes {
		if group := groups[i]; group.ID != route.Name || group.Label == "" || !slices.Equal(group.Routes, []string{route.Name}) {
			t.Errorf("want singleton for %q at %d, got %+v", route.Name, i, group)
		}
	}
}

func TestProviderGroupsExplicitConfig(t *testing.T) {
	s := testService(t)
	main := Route{Name: "custom-chat", LocalPrefix: "/chat/", Upstream: "https://example.com/chat", Enabled: true}
	tools := Route{Name: "custom-tools", LocalPrefix: "/tools/", Upstream: "https://elsewhere.example/tools", Enabled: true}
	unrelated := Route{Name: "custom", LocalPrefix: "/other/", Upstream: main.Upstream, Enabled: true}
	routes := []Route{main, unrelated, tools}
	configure(t, s, Config{Routes: routes}, nil)
	token := caller(t, s, main.Name, tools.Name)
	authority := s.snap.routes[0].authority
	cookie, csrf := loginAdmin(t, s)
	put := func() int {
		return adminRequest(s, http.MethodPut, "config", map[string]any{"revision": s.snap.revision, "routes": routes}, cookie, csrf).Code
	}
	routes[0].Provider = "invalid provider"
	if got := put(); got != http.StatusBadRequest {
		t.Fatalf("invalid provider accepted: %d", got)
	}
	routes[0].Provider, routes[2].Provider = "custom", "custom"
	if got := put(); got != http.StatusOK {
		t.Fatalf("explicit provider rejected: %d", got)
	}
	if s.snap.routes[0].authority != authority {
		t.Fatal("changing grouping metadata changed route authority")
	}
	for _, i := range []int{0, 2} {
		if _, err := s.store.authorize(token, s.snap.routes[i]); err != nil {
			t.Fatalf("provider edit invalidated grant for %s: %v", routes[i].Name, err)
		}
	}
	if _, err := s.store.authorize(token, s.snap.routes[1]); err == nil {
		t.Fatal("provider edit granted access to the unrelated route")
	}
	state := adminRequest(s, http.MethodGet, "state", nil, cookie, csrf)
	var response struct {
		Providers json.RawMessage `json:"providers"`
		Routes    []Route         `json:"routes"`
	}
	if state.Code != http.StatusOK || json.Unmarshal(state.Body.Bytes(), &response) != nil {
		t.Fatalf("GET explicit state: %d %s", state.Code, state.Body.String())
	}
	const want = `[{"id":"custom","label":"custom-chat","routes":["custom-chat","custom-tools"]},{"id":"route:custom","label":"custom","routes":["custom"]}]`
	if string(response.Providers) != want {
		t.Errorf("providers = %s, want %s", response.Providers, want)
	}
	export := adminRequest(s, http.MethodGet, "export", nil, cookie, csrf)
	cfg, err := decodeConfig(export.Body.Bytes())
	if export.Code != http.StatusOK || err != nil || len(cfg.Routes) != len(routes) || len(response.Routes) != len(routes) {
		t.Fatalf("could not round-trip route config: %d %v", export.Code, err)
	}
	for i, route := range routes {
		if cfg.Routes[i].Provider != route.Provider || response.Routes[i].Provider != route.Provider {
			t.Errorf("provider not preserved for %q in JSON or YAML", route.Name)
		}
	}
}

func TestProviderGroupsExplicitDoesNotAbsorbInferredCompanion(t *testing.T) {
	main := providerTestRoute(t, "gemini", "google-key")
	main.Provider = main.Name
	live := providerTestRoute(t, "gemini-live", "google-key")
	groups := providerGroups([]Route{main, live}, nil)
	assertProviderSingletons(t, groups, []Route{main, live})
}

func TestProviderGroupsChatGPTInstanceLabels(t *testing.T) {
	work := providerTestRoute(t, "chatgpt", "work")
	personal := providerTestRoute(t, "chatgpt", "personal")
	personal.Name = "personal-chat"
	otherWork := work
	otherWork.Name = "another-work-chat"
	backend := providerTestRoute(t, "chatgpt-backend", work.Account)
	accounts := []AccountInfo{{Name: "work", Kind: "chatgpt"}, {Name: "personal", Kind: "chatgpt"}}
	routes := []Route{work, personal, otherWork, backend}
	groups := providerGroups(routes, accounts)
	assertProviderSingletons(t, groups, routes)
	want := []string{"ChatGPT (work / chatgpt)", "ChatGPT (personal)", "ChatGPT (work / another-work-chat)", "ChatGPT plan usage and dictation"}
	for i, label := range want {
		if groups[i].Label != label {
			t.Errorf("group %q label = %q, want %q", groups[i].ID, groups[i].Label, label)
		}
	}
}

func TestProviderGroupsChatGPTProvisionKeepsOccupiedSlots(t *testing.T) {
	s := testService(t)
	occupied := Route{Name: "chatgpt-backend", LocalPrefix: "/other/", Upstream: "https://example.com/", Enabled: true, Provider: "work"}
	configure(t, s, Config{Routes: []Route{occupied}}, nil)
	account := accountRecord{AccountInfo: AccountInfo{Name: "work", Kind: "chatgpt", Generation: 1, State: "ready"}}
	if problem := s.checkNewChatGPTRoute(account.Name); problem != nil {
		t.Fatalf("group name was mistaken for an occupied route: %+v", problem)
	}
	result, problem := s.provisionChatGPTRoute(context.Background(), account)
	if problem != nil || result != "work" {
		t.Fatalf("provision %q: %+v", result, problem)
	}
	routes := s.snap.config.Routes
	if len(routes) != 3 || routes[0].Provider != "work" || routes[1].Provider != "work-2" || routes[2].Provider != "work-2" || routes[2].Name != "chatgpt-openai" {
		t.Fatalf("occupied slot or explicit provider not preserved: %+v", routes)
	}
	groups := providerGroups(routes, []AccountInfo{account.AccountInfo})
	if len(groups) != 2 || groups[0].ID != "work" || groups[1].ID != "work-2" || groups[1].Label != "ChatGPT" || !slices.Equal(groups[1].Routes, []string{"work", "chatgpt-openai"}) {
		t.Fatalf("unexpected groups for occupied slot: %+v", groups)
	}
}
