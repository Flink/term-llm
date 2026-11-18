package proxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func openCodeGoPreset(t *testing.T) routePreset {
	t.Helper()
	for _, p := range routePresets {
		if p.ID == "opencode-go" {
			return p
		}
	}
	t.Fatal("missing OpenCode Go preset")
	return routePreset{}
}

func TestOpenCodeGoPresetRules(t *testing.T) {
	p := openCodeGoPreset(t)
	want := []routePresetRule{
		{PathPrefix: "/", Upstream: "https://opencode.ai/zen/go/v1", Auth: Auth{Header: "Authorization", Prefix: "Bearer "}},
		{PathPrefix: "/messages/", Upstream: "https://opencode.ai/zen/go", Auth: Auth{Header: "x-api-key"}},
		{PathPrefix: "/gemini/", Upstream: "https://opencode.ai/inference/go/v1", Auth: Auth{Header: "x-goog-api-key"}},
	}
	if p.Label != "OpenCode Go" || p.Upstream != "" || p.Auth != (Auth{}) || !reflect.DeepEqual(p.Rules, want) || p.Usage != "auto" || p.Billing != "plan" || p.AccountKind != "" || p.WebSocket {
		t.Fatalf("wrong composite preset: %+v", p)
	}
	for _, guidance := range []string{"one key", "one grant", "three protocols", "x-opencode-client", "x-opencode-session"} {
		if !strings.Contains(strings.ToLower(p.Note), guidance) {
			t.Errorf("missing guidance %q: %s", guidance, p.Note)
		}
	}
	for _, other := range routePresets {
		if other.ID == "opencode-go-messages" || other.ID == "opencode-go-gemini" {
			t.Fatalf("legacy sibling offered for new creation: %s", other.ID)
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded routePreset
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(p, decoded) {
		t.Fatalf("preset JSON roundtrip: %s: %v", data, err)
	}
	r := presetRoute(p, "")
	snap, err := compile(Config{Routes: []Route{r}}, 1, map[string]material{"key": boundMaterial(t, want[0].Upstream, "one-vendor-key")})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.config.Routes) != 1 || len(snap.routes) != 1 || len(snap.routes[0].rules) != 3 {
		t.Fatalf("preset did not compile as one logical route: %+v", snap)
	}
	for i, rule := range snap.routes[0].rules {
		if rule.authority != snap.routes[0].authority || rule.headers.Get(want[i].Auth.Header) != want[i].Auth.Prefix+"one-vendor-key" {
			t.Errorf("rule lacks shared authority/credential: %+v", rule)
		}
	}
}
