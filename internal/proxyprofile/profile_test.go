package proxyprofile

import (
	"slices"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/providerspec"
)

func TestEveryBuiltinProviderHasProxyDecision(t *testing.T) {
	for _, spec := range providerspec.All() {
		if _, ok := ForProvider(spec.Type); !ok {
			t.Errorf("missing proxy decision for %s", spec.Type)
		}
	}
	if _, ok := ForProvider(providerspec.TypeOpenAICompat); !ok {
		t.Fatal("missing custom compatible provider decision")
	}
}
func TestProfileIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		if p.ID == "" || seen[p.ID] {
			t.Fatalf("duplicate/empty profile %q", p.ID)
		}
		seen[p.ID] = true
	}
}

func TestProfileMetadata(t *testing.T) {
	families := []string{"inference", "search", "media", "embeddings", "classification"}
	for _, p := range All() {
		t.Run(p.ID, func(t *testing.T) {
			if !slices.Contains(families, p.Family) {
				t.Errorf("invalid family %q", p.Family)
			}
			if p.Parent != "" {
				parent, ok := ByID(p.Parent)
				if !ok || parent.Parent != "" || parent.Family != p.Family {
					t.Errorf("parent %q must be a primary profile in family %q", p.Parent, p.Family)
				}
			}
			for slot, selector := range p.Slots {
				if !hasSlotBinding(p, slot, selector) {
					t.Errorf("slot %q selector %q has no matching binding", slot, selector)
				}
			}
		})
	}
}

func TestClassificationProfileSelectors(t *testing.T) {
	want := map[string]string{"typesafe": "typesafe", "openai-decisions": "openai", "cloudflare-clef": "cloudflare"}
	for _, p := range All() {
		if p.Family != "classification" && p.ClassifyProvider == "" {
			continue
		}
		if want[p.ID] == "" || p.ClassifyProvider != want[p.ID] || p.Family != "classification" || p.Parent != "" || p.WebSocket || len(p.Bindings) != 0 || InferenceCapable(p) {
			t.Errorf("unexpected classification profile: %+v", p)
		}
		if p.ID == "typesafe" && p.Label != "TypeSafe" || p.ID == "cloudflare-clef" && p.Upstream != "" {
			t.Errorf("unexpected preset defaults: %+v", p)
		}
		delete(want, p.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing classification profiles %v", want)
	}
}

func hasSlotBinding(p Profile, slot, selector string) bool {
	if selector == "" {
		return false
	}
	namespace := slot
	switch slot {
	case "classify":
		return p.ClassifyProvider == selector
	case "fetch":
		namespace = "search" // Both fetch and search live under SearchConfig.
	case "search", "embed", "image", "audio", "music", "transcription":
	default:
		return false // In particular, live integrations are not slots.
	}
	if slot == "transcription" && string(p.Provider) == selector {
		return true // OpenAI transcription uses the named inference provider.
	}
	for _, b := range p.Bindings {
		if strings.HasPrefix(b.Root, namespace+"."+selector+".") ||
			slot == "transcription" && strings.HasPrefix(b.Root, "providers."+selector+".") {
			return true // Mistral transcription uses an explicit providers binding.
		}
	}
	return false
}

func TestProfileLookupAndCompanions(t *testing.T) {
	p, ok := ForProvider(providerspec.TypeClaudeBin)
	if !ok || p.ID != "claude-bin" || p.Account != "claude-bin" {
		t.Fatalf("unexpected Claude profile: %+v", p)
	}
	if _, ok := ByID("not-a-profile"); ok {
		t.Fatal("lookup found an unknown profile")
	}
	for parent, want := range map[string][]string{
		"exa": {"exa-mcp"}, "jina": {"jina-reader"}, "chatgpt": {"chatgpt-backend", "chatgpt-openai"},
		"gemini": {"gemini-live"}, "grok": {"grok-bin"}, "opencode-go": nil,
	} {
		var got []string
		for _, companion := range Companions(parent) {
			got = append(got, companion.ID)
			if id := CanonicalParent(companion.ID); id != parent {
				t.Errorf("CanonicalParent(%q) = %q, want %q", companion.ID, id, parent)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("Companions(%q) = %v, want %v", parent, got, want)
		}
		if id := CanonicalParent(parent); id != parent {
			t.Errorf("CanonicalParent(%q) = %q, want unchanged ID", parent, id)
		}
	}
	if id := CanonicalParent("not-a-profile"); id != "not-a-profile" {
		t.Errorf("CanonicalParent changed an unknown ID to %q", id)
	}
}

func TestInferenceCapable(t *testing.T) {
	for id, want := range map[string]bool{
		"openai": true, "mistral": true, "grok-bin": true,
		"chatgpt-backend": false, "gemini-live": false, "exa": false, "jina-reader": false,
	} {
		p, ok := ByID(id)
		if !ok || InferenceCapable(p) != want {
			t.Errorf("InferenceCapable(%q) = %t, want %t (profile found: %t)", id, InferenceCapable(p), want, ok)
		}
	}
}

// api.openai.com embeddings and realtime reached with a ChatGPT OAuth token have
// no evidence of subscription inclusion, so they must not be presented as plan usage.
func TestChatGPTOpenAIAncillaryBillingIsNotClaimedAsPlan(t *testing.T) {
	for _, p := range All() {
		if p.ID != "chatgpt-openai" {
			continue
		}
		if p.Billing != "unspecified" || strings.Contains(strings.ToLower(p.Label), "plan") {
			t.Fatalf("billing=%q label=%q", p.Billing, p.Label)
		}
		return
	}
	t.Fatal("chatgpt-openai profile missing")
}

// A CLI provider reaches the proxy only through its adapter marker. Grok CLI is
// ungated on real-executable fixture evidence and shares the native Grok plan
// account; Antigravity is ungated for personal subscriptions, and Cursor and Vibe stay gated.
func TestCLIProfilesNameTheirAdapterMarkerAndGateUnverifiedClients(t *testing.T) {
	gated := map[providerspec.Type]bool{providerspec.TypeCursorBin: true, providerspec.TypeVibeBin: true}
	for _, kind := range []providerspec.Type{providerspec.TypeClaudeBin, providerspec.TypeCursorBin, providerspec.TypeGrokBin, providerspec.TypeAgyBin, providerspec.TypeVibeBin} {
		p, ok := ForProvider(kind)
		if !ok || !strings.HasPrefix(p.CLIRootEnv, "TERM_LLM_") || !strings.HasSuffix(p.CLIRootEnv, "_PROXY_ROOT") {
			t.Errorf("%s has no adapter marker: %q", kind, p.CLIRootEnv)
		}
		if gated[kind] != (p.Gate != "") {
			t.Errorf("%s gate = %q", kind, p.Gate)
		}
	}
	if grok, _ := ForProvider(providerspec.TypeGrokBin); grok.Account != "grok" || grok.Upstream == "" {
		t.Errorf("grok-bin must bind the Grok plan account kind: %+v", grok)
	}
	for _, p := range All() {
		if strings.HasPrefix(p.ID, "cursor-bin-") && p.Gate == "" {
			t.Errorf("%s must stay gated with cursor-bin", p.ID)
		}
		if p.CLIRootEnv != "" && p.Provider == "" {
			t.Errorf("%s names an adapter marker without a provider", p.ID)
		}
	}
}

func TestAntigravityPersonalCLIProfile(t *testing.T) {
	p, ok := ForProvider(providerspec.TypeAgyBin)
	if !ok || p.ID != "agy-bin" || p.Account != "agy-bin" || p.Upstream != "https://daily-cloudcode-pa.googleapis.com" ||
		p.Header != "Authorization" || p.Prefix != "Bearer " || p.Billing != "plan" || p.WebSocket ||
		p.CLIRootEnv != "TERM_LLM_AGY_PROXY_ROOT" || p.Gate != "" || p.Label != "Antigravity" {
		t.Fatalf("unexpected Personal Antigravity profile: %+v", p)
	}
}
