package usagetap

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// Reduced from a live Venice Qwen response. Null is an absent optional tier,
// not evidence that its otherwise explicit model identity is ambiguous.
func TestChatNullableServiceTierPreservesReportedVeniceUsage(t *testing.T) {
	const data = `{"model":"qwen-3-8-27b","service_tier":null,"usage":{"prompt_tokens":94,"completion_tokens":16,"total_tokens":110,"completion_tokens_details":{"reasoning_tokens":16}}}`
	for _, format := range []Format{JSON, SSE} {
		t.Run(string(format), func(t *testing.T) {
			p := New(ChatCompletions, format)
			if format == SSE {
				p.Feed([]byte("data: " + data + "\n\ndata: [DONE]\n\n"))
			} else {
				p.Feed([]byte(data))
			}
			got := p.Finish(true)
			if got.Source != Reported || got.Ambiguous || got.Model != "qwen-3-8-27b" || got.Tier != "" || got.Input != 94 || got.Output != 16 || got.Reasoning != 16 || got.Total != 110 {
				t.Fatalf("nullable tier discarded valid accounting: %+v", got)
			}
		})
	}
}

func TestChatNullTierDoesNotEraseAssertedTier(t *testing.T) {
	p := New(ChatCompletions, SSE)
	p.FeedEvent([]byte(`{"model":"model","service_tier":"flex"}`))
	p.FeedEvent([]byte(`{"model":"model","service_tier":null,"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	got := p.Finish(true)
	if got.Ambiguous || got.Model != "model" || got.Tier != "flex" {
		t.Fatal(got)
	}
}

func TestVeniceChargeJSON(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		micros     int64 // -1 denotes missing evidence.
		source     Source
	}{
		{"verified shape", `{"usage":{"prompt_tokens":1204,"completion_tokens":10,"total_tokens":1214,"prompt_tokens_details":{"cached_tokens":0},"cost":{"usd":0,"diem":0.0002498}}}`, 250, Reported},
		{"zero", `{"usage":{"cost":{"usd":0,"diem":0.0}}}`, 0, Unknown},
		{"single usd", `{"usage":{"cost":{"usd":1.125}}}`, 1_125_000, Unknown},
		{"single diem", `{"usage":{"cost":{"diem":2}}}`, 2_000_000, Unknown},
		{"round only after sum", `{"usage":{"cost":{"usd":2E-7,"diem":3e-7}}}`, 1, Unknown},
		{"exact boundary", `{"usage":{"cost":{"usd":0.0000003,"diem":0.00000019999999999999999999999999999}}}`, 0, Unknown},
		{"no counters", `{"usage":{"cost":{"usd":0.001}}}`, 1000, Unknown},
		{"malformed counters", `{"usage":{"prompt_tokens":"malformed","cost":{"usd":0.001}}}`, 1000, Unknown},
		{"malformed identity", `{"model":{},"usage":{"prompt_tokens":12,"cost":{"usd":0.001}}}`, 1000, Reported},
		{"max int64 after rounding", `{"usage":{"cost":{"usd":9223372036854.7758065}}}`, math.MaxInt64, Unknown},
		{"overflow after rounding", `{"usage":{"cost":{"usd":9223372036854.7758075}}}`, -1, Unknown},
		{"overflow from sum", `{"usage":{"cost":{"usd":9223372036854.775807,"diem":0.000001}}}`, -1, Unknown},
		{"negative", `{"usage":{"prompt_tokens":1,"cost":{"usd":1,"diem":-0.1}}}`, -1, Reported},
		{"negative zero", `{"usage":{"cost":{"usd":-0}}}`, -1, Unknown},
		{"numeric string", `{"usage":{"cost":{"usd":"0.1"}}}`, -1, Unknown},
		{"null component", `{"usage":{"cost":{"usd":null,"diem":0}}}`, -1, Unknown},
		{"oversized amount", `{"usage":{"cost":{"usd":0.` + strings.Repeat("0", 80) + `1}}}`, -1, Unknown},
		{"oversized exponent", `{"usage":{"cost":{"diem":1e-999999999}}}`, -1, Unknown},
		{"oversized cost object", `{"usage":{"cost":{` + strings.Repeat(" ", 512) + `"usd":1}}}`, -1, Unknown},
		{"empty cost", `{"usage":{"cost":{}}}`, -1, Unknown},
		{"null cost", `{"usage":{"cost":null}}`, -1, Unknown},
		{"unexpected denomination", `{"usage":{"cost":{"eur":10,"diem":0}}}`, -1, Unknown},
		{"duplicate denomination", `{"usage":{"cost":{"usd":0,"\u0075sd":0}}}`, -1, Unknown},
		{"duplicate cost", `{"usage":{"cost":{"usd":2},"\u0063ost":{"usd":0}}}`, -1, Unknown},
		{"duplicate usage", `{"usage":null,"\u0075sage":{"cost":{"usd":0}}}`, -1, Unknown},
		{"ignore diam", `{"usage":{"cost":{"usd":0.125,"diem":0.25,"diam":10}}}`, 375_000, Unknown},
		{"diam only is not a charge", `{"usage":{"cost":{"diam":0}}}`, -1, Unknown},
		{"root cost", `{"cost":{"usd":0.1},"usage":{"prompt_tokens":1}}`, 100_000, Reported},
		{"root cost without counters", `{"cost":{"usd":0.1}}`, 100_000, Unknown},
		{"duplicate usage with root cost", `{"cost":{"usd":0},"usage":{"cost":{"usd":2}},"\u0075sage":null}`, -1, Unknown},
		{"duplicate root cost", `{"cost":{"usd":2},"\u0063ost":{"usd":0}}`, -1, Unknown},
		{"prefer nested cost when both are present", `{"cost":{"usd":0.1},"usage":{"cost":{"usd":0.000001}}}`, 1, Unknown},
		{"duplicate nested cost with root cost", `{"cost":{"usd":0},"usage":{"cost":{"usd":2},"\u0063ost":{"usd":0}}}`, -1, Unknown},
		{"malformed JSON", `{"usage":{"cost":{"usd":0.001}}`, -1, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ForVeniceChatCompletions("application/json")
			p.Feed([]byte(tc.data))
			u := p.Finish(true)
			assertVeniceMicros(t, u, tc.micros)
			if u.Source != tc.source {
				t.Fatalf("token source = %q, want %q", u.Source, tc.source)
			}
			encoded, err := json.Marshal(u)
			if err != nil || strings.Contains(string(encoded), `"venice_cost_micros":`) != (tc.micros >= 0) {
				t.Fatalf("wrong JSON: %s (%v)", encoded, err)
			}
		})
	}
}

func TestVeniceChargeFinalSSE(t *testing.T) {
	const earlier = "data: {\"usage\":{\"cost\":{\"diem\":0.00005}}}\r\n\r\n"
	const final = "data: {\"choices\":[],\r\ndata: \"usage\":{\"prompt_tokens\":1204,\"cost\":{\"usd\":0,\"diem\":0.0002498}}}\r\n\r\n"
	const rootCost = `data: {"model":"gemini-4-carbon","choices":[{"delta":{"content":"Hello!"}}]}

data: {"model":"gemini-4-carbon","choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"model":"gemini-4-carbon","choices":[],"usage":{"prompt_tokens":1674,"completion_tokens":163,"total_tokens":1837},"cost":{"usd":0,"diem":0.006222500000000001}}

`
	const done = "data: [DONE]\r\n\r\n"
	for _, tc := range []struct {
		name, data string
		complete   bool
		want       int64
	}{
		{"top-level cost on final usage chunk", rootCost + done, true, 6223},
		{"cumulative replay", earlier + final + final + done + ": keepalive\n\n", true, 250},
		{"final without DONE", earlier + final, true, 250},
		{"no cost on final chunk", earlier + "data: {\"usage\":null}\n\n" + done, true, -1},
		{"invalid final cost", earlier + "data: {\"usage\":{\"cost\":{\"diem\":\"0.00005\"}}}\n\n" + done, true, -1},
		{"unexpected event after DONE", earlier + done + final, true, -1},
		{"framing failure before final", "data: broken\n\n" + final + done, true, -1},
		{"oversized event", final + "data: " + strings.Repeat("x", MaxEventBytes) + "\n\n" + done, true, -1},
		{"incomplete transport", earlier + final + done, false, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise missing-Content-Type detection, split CRLF and payload reads.
			p := ForVeniceChatCompletions("")
			for off := 0; off < len(tc.data); off += 7 {
				p.Feed([]byte(tc.data[off:min(off+7, len(tc.data))]))
			}
			assertVeniceMicros(t, p.Snapshot(), -1)
			u := p.Finish(tc.complete)
			assertVeniceMicros(t, u, tc.want)
			if u.VeniceCostMicros != nil {
				*u.VeniceCostMicros = -1 // Snapshot values do not share mutable charge state.
				assertVeniceMicros(t, p.Finish(tc.complete), tc.want)
			}
		})
	}
}

func assertVeniceMicros(t *testing.T, u Usage, want int64) {
	t.Helper()
	if want < 0 {
		if u.VeniceCostMicros != nil {
			t.Fatalf("unexpected charge: %d micros", *u.VeniceCostMicros)
		}
		return
	}
	if u.VeniceCostMicros == nil {
		t.Fatalf("no charge, want %d micros", want)
	}
	if *u.VeniceCostMicros != want {
		t.Fatalf("charge = %d micros, want %d", *u.VeniceCostMicros, want)
	}
}
