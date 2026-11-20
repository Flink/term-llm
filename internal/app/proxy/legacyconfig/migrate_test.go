package legacyconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMigrateProviders(t *testing.T) {
	const raw = `{
		"routes":[
			{"name":"chatgpt-backend","upstream":"https://chatgpt.com/backend-api","account":"plan"},
			{"name":"chatgpt","upstream":"https://chatgpt.com/backend-api/codex","account":"plan"},
			{"name":"inference-work","provider":"work","upstream":"https://gateway.example/v1"},
			{"name":"bedrock","upstream":"https://bedrock-runtime.us-east-2.amazonaws.com","account":"aws"},
			{"name":"ollama","upstream":"https://ollama.example"},
			{"name":"openai","upstream":"https://gateway.example/v1","set_headers":{"X-Literal":{"value":"synthetic-literal"}}},
			{"name":"flux-custom","upstream":"https://api.bfl.ai/v1","set_headers":{"X-Key":{"secret":"art"}}}
		],
		"providers":{"chatgpt-backend":{"model":"plan-model"},"work":{"type":"openai_compatible"}},
		"secrets":{"art":{"stored":"art","origin":"https://api.bfl.ai:443"}}
	}`
	got, renames, err := Migrate([]byte(raw), map[string]string{"plan": "chatgpt", "aws": "aws-sigv4"})
	if err != nil || renames.ClaudeRoute {
		t.Fatalf("migrate: %v, %+v", err, renames)
	}
	var result struct {
		Routes    []struct{ Name, Profile, Provider string }
		Providers map[string]provider
		Secrets   map[string]map[string]string
	}
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatal(err)
	}
	want := [][3]string{{"chatgpt-backend", "chatgpt-backend", "chatgpt"}, {"chatgpt", "chatgpt", "chatgpt"}, {"inference-work", "", "work"}, {"bedrock", "bedrock", "bedrock"}, {"ollama", "ollama", "ollama"}, {"openai", "openai", "openai"}, {"flux-custom", "flux", "flux"}}
	if len(result.Routes) != len(want) || len(result.Providers) != 6 {
		t.Fatal("wrong route or provider count")
	}
	for i, r := range result.Routes {
		if [3]string{r.Name, r.Profile, r.Provider} != want[i] {
			t.Errorf("route %d = %+v, want %v", i, r, want[i])
		}
	}
	if p := result.Providers["chatgpt"]; p.Label != "ChatGPT" || p.Client == nil || p.Client.Model != "plan-model" {
		t.Errorf("companion hint not moved: %+v", p)
	}
	if p := result.Providers["work"]; p.Client == nil || p.Client.Type != "openai_compatible" {
		t.Errorf("explicit group hint not kept: %+v", p)
	}
	if art := result.Secrets["art"]; len(art) != 1 || art["stored"] != "art" || !strings.Contains(string(got), `"value":"synthetic-literal"`) {
		t.Fatal("lost routing material or retained secret origin")
	}
}

func TestMigrateRejectsAmbiguousLegacyRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, routes, hints string
		want                []string
	}{
		{"route collision", `{"name":"group"},{"name":"work","provider":"group"}`, "{}", []string{"group", "work", "rename"}},
		{"conflicting hints", `{"name":"work","provider":"group"}`, `{"work":{"model":"a"},"group":{"model":"b"}}`, []string{"group", "work", "choose one hint"}},
		{"mixed explicit group", `{"name":"openai","provider":"group"},{"name":"ollama","provider":"group"}`, "{}", []string{"group", "openai", "ollama", "split"}},
		{"explicit native group", `{"name":"claude-code","account":"plan","provider":"group"}`, "{}", []string{"group", "claude-code", "remove"}},
		{"canonical name collision", `{"name":"art-a","upstream":"https://api.bfl.ai/v1","set_headers":{"X-Key":{"secret":"one"}}},{"name":"art-b","upstream":"https://api.bfl.ai/v1","set_headers":{"X-Key":{"secret":"two"}}}`, "{}", []string{"art-a", "art-b", "flux"}},
		{"occupied Claude name", `{"name":"claude-code","account":"plan"},{"name":"claude-bin"}`, "{}", []string{"claude-code", "claude-bin", "occupied"}},
		{"occupied Claude prefix", `{"name":"claude-code","account":"plan"},{"name":"custom","local_prefix":"/claude-bin/"}`, "{}", []string{"claude-code", "custom", "occupied"}},
		{"route star", `{"name":"work","allow":["GET /v1/*"],"set_headers":{"X-Token":{"value":"keep-private"}}}`, "{}", []string{"work", "Allow", "re-approve"}},
		{"rule star", `{"name":"work","rules":[{"allow":["GET /v1/*"],"set_headers":{"X-Token":{"value":"keep-private"}}}]}`, "{}", []string{"work", "Allow", "re-approve"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"routes":[` + tc.routes + `],"providers":` + tc.hints + `}`
			got, renames, err := Migrate([]byte(raw), map[string]string{"plan": "claude-code"})
			if err == nil || len(got) != 0 || renames.ClaudeRoute {
				t.Fatalf("expected abort, got %v, %+v", err, renames)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected %q in %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "keep-private") || strings.Contains(err.Error(), "/v1/*") {
				t.Fatal("error echoed file values")
			}
		})
	}
}

func TestMigrateClaudeWithCustomPrefix(t *testing.T) {
	raw := []byte(`{"routes":[{"name":"claude-code","account":"plan","local_prefix":"/custom/"},{"name":"claude-code-2","account":"plan2","local_prefix":"/claude-code-2/"}],"providers":{"claude-code":{"model":"shared-model"}}}`)
	got, renames, err := Migrate(raw, map[string]string{"plan": "claude-code", "plan2": "claude-code"})
	if err != nil || !renames.ClaudeRoute {
		t.Fatalf("Claude migration: %v, %+v", err, renames)
	}
	for _, want := range []string{`"name":"claude-bin"`, `"name":"claude-code-2"`, `"profile":"claude-bin"`, `"local_prefix":"/custom/"`, `"local_prefix":"/claude-code-2/"`, `"client":{"model":"shared-model"}`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestCheckLegacyFile(t *testing.T) {
	raw := `routes:
  - name: work
    set_headers:
      X-Foo: { value: keep-private }
  - name: backup
    provider: model-work
  - name: openai
    provider: openai
providers:
  openai: {model: keep-private}
secrets:
  example: { origin: 'keep-private', file: keep-private }
`
	err := CheckLegacyFile([]byte(raw))
	if err == nil {
		t.Fatal("legacy operator file accepted")
	}
	for _, want := range []string{"work", "backup", "openai", "provider", "profile", "model-work", "under client", "secret origin binding was removed; delete this line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "keep-private") {
		t.Fatal("error echoed file values")
	}
	if err := CheckLegacyFile([]byte(`routes: [{name: work, provider: work}]
providers: {work: {label: Work, client: {type: openai_compatible}}}
secrets: {example: {file: not-disclosed}}`)); err != nil {
		t.Fatal("hand-made route unexpectedly requires a profile:", err)
	}
}
