package proxyprofile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/providerspec"
)

const sampleDeepSeekLocalYAML = `
cdck_deepseek:
    enabled: false
    type: vllm
    base_url: https://snorlax1b.dub2.discourse.cloud:34567/v1
    api_key: $(op-cache read "op://Everybody/Snorlax LLM/credential" --account discourse.1password.com)
    model: deepseek-ai/DeepSeek-V4-Flash
    context_window: 200000
    max_output_tokens: 50000
    vllm_thinking_param: thinking
    vision_via: cdck_qwen
    models:
      - id: deepseek-ai/DeepSeek-V4-Flash
        alias: deepseek-v4-flash
        reasoning_efforts: [none, low, high, max]
        default_reasoning_effort: high
      - deepseek-ai/DeepSeek-V4-Lite
`

func TestImportTermLLMProviderYAML(t *testing.T) {
	parsed, err := ImportTermLLMProviderYAML([]byte(sampleDeepSeekLocalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "cdck_deepseek" {
		t.Fatalf("Name = %q, want cdck_deepseek", parsed.Name)
	}
	if parsed.Upstream != "https://snorlax1b.dub2.discourse.cloud:34567/v1" {
		t.Fatalf("Upstream = %q", parsed.Upstream)
	}
	wantStripped := "api_key,base_url,enabled"
	if got := strings.Join(parsed.Stripped, ","); got != wantStripped {
		t.Fatalf("Stripped = %q, want %q", got, wantStripped)
	}
	if parsed.Config.Type != providerspec.TypeVLLM || parsed.Config.Model != "deepseek-ai/DeepSeek-V4-Flash" {
		t.Fatalf("unexpected Type/Model: %+v", parsed.Config)
	}
	if parsed.Config.ContextWindow != 200000 || parsed.Config.MaxOutputTokens != 50000 || parsed.Config.VLLMThinkingParam != "thinking" || parsed.Config.VisionVia != "cdck_qwen" {
		t.Fatalf("unexpected metadata: %+v", parsed.Config)
	}
	if len(parsed.Config.Models) != 2 {
		t.Fatalf("Models len = %d, want 2", len(parsed.Config.Models))
	}
	if parsed.Config.Models[0].Alias != "deepseek-v4-flash" || parsed.Config.Models[0].DefaultReasoningEffort != "high" {
		t.Fatalf("first model = %+v", parsed.Config.Models[0])
	}
	if !parsed.Config.Models[1].IsSimple() || parsed.Config.Models[1].ID != "deepseek-ai/DeepSeek-V4-Lite" {
		t.Fatalf("second model = %+v", parsed.Config.Models[1])
	}
	if strings.Contains(parsed.ConfigYAML, "op-cache") || strings.Contains(parsed.ConfigYAML, "base_url") {
		t.Fatalf("stripped fields leaked into ConfigYAML:\n%s", parsed.ConfigYAML)
	}
	// Round-trip canonical YAML through strict parser.
	reparsed, err := ParseClientProviderYAML([]byte(parsed.ConfigYAML))
	if err != nil {
		t.Fatalf("reparsing canonical YAML failed: %v\n%s", err, parsed.ConfigYAML)
	}
	if reparsed.Model != parsed.Config.Model || len(reparsed.Models) != 2 {
		t.Fatalf("round-trip mismatch: %+v", reparsed)
	}
}

func TestImportTermLLMProviderYAMLProvidersWrapperAndBare(t *testing.T) {
	var indented strings.Builder
	indented.WriteString("providers:\n")
	for _, line := range strings.Split(sampleDeepSeekLocalYAML, "\n") {
		if strings.TrimSpace(line) != "" {
			indented.WriteString("  " + line + "\n")
		}
	}
	parsed, err := ImportTermLLMProviderYAML([]byte(indented.String()))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "cdck_deepseek" || parsed.Config.Type != providerspec.TypeVLLM {
		t.Fatalf("wrapped parse = %+v", parsed)
	}

	bare := "type: vllm\nbase_url: https://vllm.example.test/v1\nmodel: m1\n"
	bareParsed, err := ImportTermLLMProviderYAML([]byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if bareParsed.Name != "" || bareParsed.Upstream != "https://vllm.example.test/v1" || bareParsed.Config.Model != "m1" {
		t.Fatalf("bare parse = %+v", bareParsed)
	}
}

// A local base_url may be the full chat endpoint; the route upstream must be
// the API base so the client's appended /chat/completions is not doubled.
func TestImportTermLLMProviderYAMLStripsChatCompletionsEndpoint(t *testing.T) {
	for _, base := range []string{
		"srv://_api._tcp.example.test/class/frontier/v1/chat/completions",
		"srv://_api._tcp.example.test/class/frontier/v1/chat/completions/",
		" srv://_api._tcp.example.test/class/frontier/v1/ ",
	} {
		parsed, err := ImportTermLLMProviderYAML([]byte("type: vllm\nbase_url: \"" + base + "\"\nmodel: m1\n"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "srv://_api._tcp.example.test/class/frontier/v1"; parsed.Upstream != want {
			t.Fatalf("Upstream for %q = %q, want %q", base, parsed.Upstream, want)
		}
	}
}

func TestParseClientProviderYAMLRejectsForbiddenAndUnknownFields(t *testing.T) {
	for _, field := range []string{
		"api_key: secret",
		"base_url: https://evil.example.test/v1",
		"url: https://evil.example.test/chat",
		"enabled: false",
		"env:\n  FOO: bar",
		"headers:\n  Authorization: Bearer x",
		"proxy:\n  url: https://other.example.test/x",
		"access_key_id: AKIA",
		"secret_access_key: secret",
		"enable_hooks: true",
		"unknown_custom_field: 123",
	} {
		if _, err := ParseClientProviderYAML([]byte("type: vllm\n" + field + "\n")); err == nil {
			t.Errorf("accepted disallowed field: %s", field)
		}
	}
}

func TestClientProviderConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  ClientProviderConfig
		want string
	}{
		{"unknown type", ClientProviderConfig{Type: "bogus"}, "unsupported provider type"},
		{"gated type", ClientProviderConfig{Type: providerspec.TypeCursorBin}, "cannot be advertised"},
		{"bad reasoning", ClientProviderConfig{Reasoning: "always"}, "reasoning must be"},
		{"negative context", ClientProviderConfig{ContextWindow: -1}, "must not be negative"},
		{"wildcard allowed model", ClientProviderConfig{AllowedModels: []string{"gpt-*"}}, "allowed_models"},
		{"missing effort", ClientProviderConfig{Models: []ClientModelEntry{{ID: "m", ReasoningEfforts: []string{"low"}, DefaultReasoningEffort: "high"}}}, "not listed in reasoning_efforts"},
		{"empty model", ClientProviderConfig{Models: []ClientModelEntry{{}}}, "must specify id or alias"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestClientModelEntryJSONRoundTrip(t *testing.T) {
	in := ClientProviderConfig{
		Type:  providerspec.TypeVLLM,
		Model: "deepseek-ai/DeepSeek-V4-Flash",
		Models: []ClientModelEntry{
			{ID: "simple-model"},
			{ID: "deepseek-ai/DeepSeek-V4-Flash", Alias: "deepseek-v4-flash", ReasoningEfforts: []string{"none", "high"}, DefaultReasoningEffort: "high"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"simple-model"`) {
		t.Fatalf("simple model did not serialize as string: %s", b)
	}
	var out ClientProviderConfig
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 2 || !out.Models[0].IsSimple() || out.Models[1].Alias != "deepseek-v4-flash" {
		t.Fatalf("JSON round-trip mismatch: %+v", out.Models)
	}
}
