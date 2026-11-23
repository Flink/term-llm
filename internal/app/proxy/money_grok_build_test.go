package proxy

import (
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestMoneyGrokBuildPlanCatalog(t *testing.T) {
	s := testService(t)
	s.pricing = proxyCatalog(t, `{"xai":{"api":"https://api.x.ai/v1","models":{
		"grok-4.7":{"cost":{"input":2,"output":6,"cache_read":0.5,"tiers":[{"input":4,"output":12,"cache_read":1,"tier":{"type":"context","size":200000}}]}},
		"grok-4.8":{"cost":{"input":3,"output":8,"cache_read":0.75,"tiers":[{"input":6,"output":16,"cache_read":1.5,"tier":{"type":"context","size":200000}}]}},
		"grok-4.9":{"cost":{"input":5,"output":10,"cache_read":1}},
		"grok-4.9-build":{"cost":{"input":1,"output":2,"cache_read":0.25}},
		"grok-5":{"cost":{"input":2,"output":6,"cache_read":0.5}},
		"grok-5.0":{}
	}}}`)
	for _, tt := range []struct {
		name, model, note, source string
		input, want               int64
		changeRoute               func(*Route)
		changeUsage               func(*ObservedUsage)
	}{
		{name: "current", model: "grok-4.7-build", input: 17626, want: 23726, note: "public grok-4.7"},
		{name: "current below cutoff", model: "grok-4.7-build", input: 199999, want: 388472, note: "public grok-4.7"},
		{name: "current at cutoff", model: "grok-4.7-build", input: 200000, want: 776948, note: "public grok-4.7"},
		{name: "current Fast with catalog", model: "grok-4.7-build-fast", input: 17626, want: 47452, source: "https://docs.x.ai/developers/pricing", note: "Fast is not available on the public API"},
		{name: "major-only version", model: "grok-5-build", input: 17626, want: 23726, note: "public grok-5"},
		{name: "major-only Fast", model: "grok-5-build-fast", input: 17626},
		{name: "future", model: "grok-4.8-build", input: 17626, want: 35078, note: "public grok-4.8"},
		{name: "future long context", model: "grok-4.8-build", input: 200001, want: 1164406, note: "public grok-4.8"},
		{name: "exact entry wins", model: "grok-4.9-build", input: 17626, want: 11352},
		{name: "ordinary public ID", model: "grok-4.7", input: 17626, want: 23726},
		{name: "future Fast never borrows standard rates", model: "grok-4.8-build-fast", input: 17626},
		{name: "missing base model", model: "grok-9.9-build", input: 17626},
		{name: "base model without prices", model: "grok-5.0-build", input: 17626},
		{name: "nonstandard suffix", model: "grok-4.8-preview-build", input: 17626},
		{name: "not on public API", model: "grok-4.8-build", input: 17626, changeRoute: func(r *Route) { r.Upstream = "https://api.x.ai/v1" }},
		{name: "wrong protocol", model: "grok-4.8-build", input: 17626, changeUsage: func(o *ObservedUsage) { o.Protocol = usagetap.ChatCompletions }},
		{name: "several turns without context tiers", model: "grok-5-build", input: 17626, changeUsage: func(o *ObservedUsage) { o.Usage.Turns = 2 }},
		{name: "not a plan", model: "grok-4.8-build", input: 17626, changeRoute: func(r *Route) { r.Billing = "pay_as_you_go" }},
		{name: "needs account", model: "grok-4.8-build", input: 17626, changeRoute: func(r *Route) { r.Account = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := Route{Name: "renamed", Upstream: "https://cli-chat-proxy.grok.com/v1", Account: "family", Billing: "plan"}
			if tt.changeRoute != nil {
				tt.changeRoute(&route)
			}
			o := offlineObserved(tt.model, "", tt.input, 9728, 511)
			o.Usage.CacheWrite = -1
			if tt.changeUsage != nil {
				tt.changeUsage(&o)
			}
			rec := Record{Outcome: "complete", Usage: &o}
			s.finalizeMoney(&rec, route)
			if route.Billing != "plan" {
				if rec.Money.Amount != nil || rec.Money.APIEquivalent != nil {
					t.Fatalf("Grok comparison leaked into non-plan billing: %+v", rec.Money)
				}
				return
			}
			assertPlanActualUnchanged(t, *rec.Money, "included")
			e := rec.Money.APIEquivalent
			if e == nil || e.Model != tt.model || rec.Usage.Usage.Model != tt.model {
				t.Fatalf("response identity changed: %+v", e)
			}
			if tt.want == 0 {
				if e.Amount != nil || e.Kind != "unpriced" {
					t.Fatalf("unexpected comparison: %+v", e)
				}
				return
			}
			source := tt.source
			if source == "" {
				source = "models.dev/xai"
			}
			if e.Amount == nil || *e.Amount != tt.want || e.Source != source || e.Rate == nil || e.Rate.Model != tt.model || !strings.Contains(e.Reason, tt.note) {
				t.Fatalf("incorrect catalog comparison: %+v; want %d", e, tt.want)
			}
		})
	}
}

func TestMoneyGrokBuildPlanPublishedFallback(t *testing.T) {
	const upstream = "https://cli-chat-proxy.grok.com:443/v1/"
	for _, tt := range []struct {
		model, tier string
		input, want int64
	}{
		{"grok-4.7-build", "", 17626, 23726},
		{"grok-4.7-build", "", 200000, 776948},
		{"grok-4.7-build-fast", "default", 17626, 47452},
		{"grok-4.7-build-fast", "standard", 200000, 776948},
		{"grok-4.7-build-fast", "default", 200001, 1165428},
		{"grok-4.7-build-fast", "priority", 17626, 0},
	} {
		route := Route{Name: "grok-bin", Upstream: upstream, Account: "family", Billing: "plan"}
		o := offlineObserved(tt.model, tt.tier, tt.input, 9728, 511)
		m := finishRequestMoney(offlineMoneyInput(route, o), route, &o, nil)
		assertPlanActualUnchanged(t, m, "included")
		e := m.APIEquivalent
		if tt.want == 0 {
			if e.Amount != nil || e.Kind != "unpriced" {
				t.Fatalf("unexpected comparison: %+v", e)
			}
			continue
		}
		if e.Amount == nil || *e.Amount != tt.want || e.AsOf != "2026-10-10" || e.Source != "https://docs.x.ai/developers/pricing" || e.Rate == nil || e.Rate.Model != tt.model {
			t.Fatalf("incorrect published comparison: %+v; want %d", e, tt.want)
		}
		if strings.HasSuffix(tt.model, "-fast") && !strings.Contains(e.Reason, "Fast is not available on the public API") {
			t.Fatalf("Fast provenance missing: %+v", e)
		}
	}
}

func TestMoneyGrokBuildPlanFallbackGuards(t *testing.T) {
	route := Route{Name: "grok", Upstream: "https://cli-chat-proxy.grok.com/v1", Account: "family", Billing: "plan"}
	o := offlineObserved("grok-4.7-build-fast", "default", 17626, 9728, 511)
	for name, change := range map[string]func(*Route, *ObservedUsage){
		"wrong origin":   func(r *Route, _ *ObservedUsage) { r.Upstream = "https://cli-chat-proxy.grok.com.example/v1" },
		"not a plan":     func(r *Route, _ *ObservedUsage) { r.Billing = "pay_as_you_go" },
		"wrong protocol": func(_ *Route, o *ObservedUsage) { o.Protocol = usagetap.ChatCompletions },
		"several turns":  func(_ *Route, o *ObservedUsage) { o.Usage.Turns = 2 },
		"unknown cache":  func(_ *Route, o *ObservedUsage) { o.Usage.CachedInput = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			r, usage := route, o
			change(&r, &usage)
			m := finishRequestMoney(offlineMoneyInput(r, usage), r, &usage, nil)
			if m.Amount != nil || (m.APIEquivalent != nil && m.APIEquivalent.Amount != nil) {
				t.Fatalf("unexpected price: %+v", m)
			}
		})
	}

	in := offlineMoneyInput(route, o)
	card := MoneyRate{Provider: in.Provider, Model: in.Model, Tier: in.Tier, Currency: "USD", Source: "operator", AsOf: "2026-11-23", Input: moneyPtr(1_000_000), Output: moneyPtr(1_000_000), CacheRead: moneyPtr(1_000_000)}
	m := finishRequestMoney(in, route, &o, []MoneyRate{card})
	if e := m.APIEquivalent; e.Amount == nil || *e.Amount != 18137 || e.Source != "operator" {
		t.Fatalf("operator override lost precedence: %+v", e)
	}
}
