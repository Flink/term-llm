package legacyconfig

import (
	"encoding/json"
	"fmt"
)

// Freeze the emitted labels for this migration. Profiles that never used
// their old profile label as a stored default (such as OpenCode Go and Cursor)
// don't belong here: any matching label on those providers is operator-owned.
var providerLabelChangesV11 = map[string]struct{ old, new string }{
	"agy-bin":           {"Antigravity personal subscription", "Antigravity"},
	"anthropic":         {"Anthropic API", "Anthropic"},
	"bedrock":           {"AWS Bedrock", "Bedrock"},
	"chatgpt":           {"ChatGPT plan", "ChatGPT"},
	"chatgpt-backend":   {"ChatGPT plan usage and dictation", "ChatGPT Usage & Dictation"},
	"chatgpt-openai":    {"ChatGPT OAuth embeddings and live (billing unspecified)", "ChatGPT Embeddings & Live"},
	"claude-bin":        {"Claude Code plan", "Claude Code"},
	"cloudflare-clef":   {"Cloudflare Workers AI Clef", "Cloudflare Clef"},
	"copilot":           {"GitHub Copilot plan", "GitHub Copilot"},
	"gemini":            {"Google Gemini", "Gemini"},
	"grok":              {"Grok plan", "Grok"},
	"grok-bin":          {"Grok CLI plan", "Grok CLI"},
	"jina":              {"Jina embeddings", "Jina"},
	"openai":            {"OpenAI API", "OpenAI"},
	"openai-compatible": {"OpenAI-compatible server", "OpenAI-compatible"},
	"perplexity":        {"Perplexity Search", "Perplexity"},
	"typesafe":          {"TypeSafe (Jev)", "TypeSafe"},
	"voyage":            {"Voyage embeddings", "Voyage"},
	"xai":               {"xAI API", "xAI"},
}

// ProviderLabelPatchV11 returns a merge patch for old default labels, or nil
// when there are no matching defaults. It never changes custom labels or IDs.
func ProviderLabelPatchV11(data []byte) ([]byte, error) {
	var c struct {
		Routes []struct {
			Profile  string `json:"profile"`
			Provider string `json:"provider"`
		} `json:"routes"`
		Providers map[string]struct {
			Label string `json:"label"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("could not decode stored provider labels")
	}
	// Match an owned route by profile, not the provider's user-visible ID or
	// upstream. A legacy group can carry the label of a companion route.
	labels := make(map[string]map[string]string)
	for _, route := range c.Routes {
		change, ok := providerLabelChangesV11[route.Profile]
		if ok && c.Providers[route.Provider].Label == change.old {
			labels[route.Provider] = map[string]string{"label": change.new}
		}
	}
	if len(labels) == 0 {
		return nil, nil
	}
	// Patch only the label keys; keep all other configuration and grants intact.
	patch, err := json.Marshal(map[string]any{"providers": labels})
	if err != nil {
		return nil, fmt.Errorf("encode provider label updates: %w", err)
	}
	return patch, nil
}
