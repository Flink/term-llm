package usagetap

import (
	"bytes"
	"encoding/json"
)

// ForMistralChatCompletions observes the first-party Mistral chat API. The
// caller must verify the origin and endpoint before using these semantics.
func ForMistralChatCompletions(contentType string) *Parser {
	p := ForResponse("/chat/completions", contentType)
	p.mistral = true
	return p
}

func (p *Parser) mistralUsage(o object, u *Usage) {
	m := obj(o["usage"])
	if m == nil {
		return
	}
	if raw, exists := m["service_tier"]; exists && !isJSONNull(raw) {
		p.observeTier(m)
	}
	if u.CachedInput >= 0 || u.Input < 0 {
		return
	}
	raw, present := m["prompt_tokens_details"]
	details := obj(raw)
	if present && details == nil && !isJSONNull(raw) {
		p.bad = true
		return
	}
	if _, present := details["cached_tokens"]; !present {
		// Mistral documents omission as zero on a cache miss:
		// https://docs.mistral.ai/inference/conversations/advanced/prompt-caching
		u.CachedInput = 0
	}
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
