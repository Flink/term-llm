package usagetap

import "testing"

func TestMistralChatUsage(t *testing.T) {
	for _, tc := range []struct {
		name, fields, tier string
		cached             int64
		source             Source
		ambiguous          bool
	}{
		{"omitted cache", `,"service_tier":"standard"`, "standard", 0, Reported, false},
		{"cache hit", `,"prompt_tokens_details":{"cached_tokens":64}`, "", 64, Reported, false},
		{"missing cache counter", `,"prompt_tokens_details":{}`, "", 0, Reported, false},
		{"malformed details", `,"prompt_tokens_details":[]`, "", -1, Unknown, false},
		{"malformed cache counter", `,"prompt_tokens_details":{"cached_tokens":null}`, "", -1, Unknown, false},
		{"priority tier", `,"service_tier":"priority"`, "priority", 0, Reported, false},
		{"invalid tier", `,"service_tier":99`, "", 0, Reported, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ForMistralChatCompletions("application/json")
			p.Feed([]byte(`{"model":"mistral-small-2603","usage":{"prompt_tokens":100,"completion_tokens":10` + tc.fields + `}}`))
			u := p.Finish(true)
			if u.Source != tc.source || u.CachedInput != tc.cached || u.Tier != tc.tier || u.Ambiguous != tc.ambiguous {
				t.Fatalf("unexpected usage: %+v", u)
			}
		})
	}
}
