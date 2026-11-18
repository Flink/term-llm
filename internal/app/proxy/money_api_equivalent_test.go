package proxy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func assertPlanActualUnchanged(t *testing.T, m Money, kind string) {
	t.Helper()
	if m.Billing != "plan" || m.Kind != kind || m.Amount != nil || m.Currency != "" || m.Source != "" || m.AsOf != "" || m.Rate != nil {
		t.Fatalf("API comparison changed actual billing: %+v", m)
	}
}

func TestMoneyPlanAPIEquivalentOperatorCard(t *testing.T) {
	rate := testMoneyRate()
	rate.Tier = "standard"
	base := MoneyInput{Billing: "plan", Provider: rate.Provider, Model: rate.Model, Tier: rate.Tier, UsageState: "reported", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 100, CacheWriteTokens: 50,
		Charge: &MoneyCharge{AmountMicros: 999999, Currency: "EUR", Source: "actual plan overage", AsOf: "2026-11-16"}}
	tests := []struct {
		name, kind, actual string
		amount             int64
		change             func(*MoneyInput, *MoneyRate)
	}{
		{"complete", "estimated", "included", 3400, func(*MoneyInput, *MoneyRate) {}},
		{"partial positive lower bound", "estimated_floor", "unmeasured", 3400, func(in *MoneyInput, _ *MoneyRate) { in.UsageState = "partial" }},
		{"partial zero unknown", "unmeasured", "unmeasured", -1, func(in *MoneyInput, _ *MoneyRate) {
			in.UsageState = "partial"
			in.InputTokens, in.OutputTokens, in.CacheReadTokens, in.CacheWriteTokens = 0, 0, 0, 0
		}},
		{"complete known zero", "estimated", "included", 0, func(in *MoneyInput, _ *MoneyRate) {
			in.InputTokens, in.OutputTokens, in.CacheReadTokens, in.CacheWriteTokens = 0, 0, 0, 0
		}},
		{"unknown usage ignores provider charge", "unmeasured", "unmeasured", -1, func(in *MoneyInput, _ *MoneyRate) { in.UsageState = "unknown" }},
		{"unknown model", "unpriced", "included", -1, func(in *MoneyInput, _ *MoneyRate) { in.Model = "unknown" }},
		{"missing model even explicit card", "unpriced", "included", -1, func(in *MoneyInput, r *MoneyRate) { in.Model, r.Model = "", "" }},
		{"unknown tier", "unpriced", "included", -1, func(in *MoneyInput, _ *MoneyRate) { in.Tier = "priority" }},
		{"missing tier even explicit card", "unpriced", "included", -1, func(in *MoneyInput, r *MoneyRate) { in.Tier, r.Tier = "", "" }},
		{"unknown read count", "unpriced", "included", -1, func(in *MoneyInput, _ *MoneyRate) { in.CacheReadTokens = -1 }},
		{"unknown write count", "unpriced", "included", -1, func(in *MoneyInput, _ *MoneyRate) { in.CacheWriteTokens = -1 }},
		{"unpriced cache read", "unpriced", "included", -1, func(_ *MoneyInput, r *MoneyRate) { r.CacheRead = nil }},
		{"unpriced cache write", "unpriced", "included", -1, func(_ *MoneyInput, r *MoneyRate) { r.CacheWrite = nil }},
		{"overflow", "unpriced", "included", -1, func(in *MoneyInput, r *MoneyRate) { in.InputTokens = 1 << 62; r.Input = moneyPtr(maxMoneyMicros) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, card := base, rate
			tt.change(&in, &card)
			m := CalculateMoney(in, []MoneyRate{card})
			assertPlanActualUnchanged(t, m, tt.actual)
			e := m.APIEquivalent
			if e == nil || e.Kind != tt.kind || e.Model != in.Model || !strings.Contains(e.Reason, "Counterfactual") {
				t.Fatalf("comparison missing/incorrect: %+v", e)
			}
			if tt.amount < 0 {
				if e.Amount != nil || e.Currency != "" || e.Rate != nil {
					t.Fatalf("unknown became money: %+v", e)
				}
			} else if e.Amount == nil || *e.Amount != tt.amount || e.Currency != card.Currency || e.Source != card.Source || e.AsOf != card.AsOf || e.Rate == nil {
				t.Fatalf("wrong frozen comparison: %+v", e)
			}
		})
	}
	m := CalculateMoney(base, []MoneyRate{rate})
	*rate.Input, *rate.CacheRead = 9999999, 9999999
	if *m.APIEquivalent.Rate.Input != 2000000 || *m.APIEquivalent.Rate.CacheRead != 500000 || *m.APIEquivalent.Amount != 3400 {
		t.Fatal("equivalent retained mutable operator rates", m.APIEquivalent)
	}
	base.RefusedBeforeDispatch = true
	if m = CalculateMoney(base, []MoneyRate{rate}); m.Kind != "no_charge_expected" || m.APIEquivalent != nil {
		t.Fatal("blocked request assigned hypothetical activity", m)
	}
}

func TestMoneyPlanAPIEquivalentExactEndpointMapping(t *testing.T) {
	tests := []struct {
		name, upstream, account, model, tier string
		protocol                             usagetap.Protocol
		amount                               int64
	}{
		{"chatgpt", "https://chatgpt.com/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, 78500},
		{"chatgpt canonical port", "https://chatgpt.com:443/backend-api/codex/", "family", "gpt-5.3-codex", "default", usagetap.Responses, 30450},
		{"anthropic oauth", "https://api.anthropic.com", "family", "claude-opus-5", "standard", usagetap.Messages, 84250},
		{"anthropic v1", "https://api.anthropic.com:443/v1/", "family", "claude-opus-4-8", "standard", usagetap.Messages, 84250},
		{"explicit API plan", "https://api.openai.com/v1", "", "gpt-5.5", "default", usagetap.Responses, 78500},
		{"route name not provider", "https://custom.example/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"account name not provider", "https://custom.example/v1", "chatgpt", "gpt-5.5", "default", usagetap.Responses, -1},
		{"chatgpt requires account", "https://chatgpt.com/backend-api/codex", "", "gpt-5.5", "default", usagetap.Responses, -1},
		{"lookalike", "https://chatgpt.com.example/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"userinfo", "https://user@chatgpt.com/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"query", "https://chatgpt.com/backend-api/codex?x=1", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"empty query", "https://chatgpt.com/backend-api/codex?", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"fragment", "https://chatgpt.com/backend-api/codex#x", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"escaped path", "https://chatgpt.com/backend-api/%63odex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"insecure", "http://chatgpt.com/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"custom port", "https://chatgpt.com:8443/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"wrong root", "https://chatgpt.com/backend-api", "family", "gpt-5.5", "default", usagetap.Responses, -1},
		{"unknown model", "https://chatgpt.com/backend-api/codex", "family", "gpt-5.5-high", "default", usagetap.Responses, -1},
		{"unknown tier", "https://chatgpt.com/backend-api/codex", "family", "gpt-5.5", "", usagetap.Responses, -1},
		{"unsupported tier", "https://chatgpt.com/backend-api/codex", "family", "gpt-5.5", "priority", usagetap.Responses, -1},
		{"wrong protocol", "https://chatgpt.com/backend-api/codex", "family", "gpt-5.5", "default", usagetap.Gemini, -1},
		{"no invented grok price", "https://cli-chat-proxy.grok.com/v1", "family", "grok-4", "default", usagetap.Responses, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := Route{Name: "openai", Upstream: tt.upstream, Account: tt.account, Billing: "plan"}
			observed := offlineObserved(tt.model, tt.tier, 12400, 2000, 850)
			observed.Protocol = tt.protocol
			observed.Usage.Reasoning = 0
			m := finishRequestMoney(offlineMoneyInput(route, observed), route, &observed, nil)
			assertPlanActualUnchanged(t, m, "included")
			e := m.APIEquivalent
			if e == nil || e.Model != tt.model {
				t.Fatalf("exact response model missing: %+v", e)
			}
			if tt.amount < 0 {
				if e.Amount != nil || e.Kind != "unpriced" {
					t.Fatalf("guessed comparison: %+v", e)
				}
			} else if e.Kind != "estimated" || e.Amount == nil || *e.Amount != tt.amount || e.Rate == nil || e.Source == "" || e.AsOf == "" {
				t.Fatalf("incorrect public API comparison: %+v; want %d", e, tt.amount)
			}
			if tt.amount >= 0 && strings.Contains(tt.upstream, "chatgpt.com") && !strings.Contains(e.Reason, "does not assert the same product or charge") {
				t.Fatal("ChatGPT comparison not labeled", e)
			}
		})
	}
}

func TestMoneyPlanAPIEquivalentConservativeFallback(t *testing.T) {
	route := Route{Name: "home", Upstream: "https://chatgpt.com/backend-api/codex", Account: "family", Billing: "plan"}
	tests := []struct {
		name   string
		change func(*ObservedUsage)
	}{
		{"partial", func(o *ObservedUsage) { o.Usage.Source = usagetap.Partial }},
		{"ambiguous", func(o *ObservedUsage) { o.Usage.Ambiguous = true }},
		{"multiple turns", func(o *ObservedUsage) { o.Usage.Turns = 2 }},
		{"unknown cache", func(o *ObservedUsage) { o.Usage.CachedInput = -1 }},
		{"unknown nonzero write tariff", func(o *ObservedUsage) { o.Usage.Model = "gpt-5.6-terra"; o.Usage.CacheWrite = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := offlineObserved("gpt-5.5", "default", 12400, 2000, 850)
			tt.change(&o)
			in := offlineMoneyInput(route, o)
			in.Charge = &MoneyCharge{AmountMicros: 10, Currency: "USD", Source: "ignored", AsOf: "2026-11-16"}
			m := finishRequestMoney(in, route, &o, nil)
			if m.APIEquivalent == nil || m.APIEquivalent.Amount != nil {
				t.Fatalf("unsafe usage priced: %+v", m.APIEquivalent)
			}
			if m.APIEquivalent.Model != in.Model || (o.Usage.Ambiguous && m.APIEquivalent.Model != "") {
				t.Fatalf("ambiguous or guessed response model: %+v", m.APIEquivalent)
			}
		})
	}
	o := offlineObserved("gpt-5.5", "default", 12400, 2000, 850)
	card := MoneyRate{Provider: route.Name, Model: o.Usage.Model, Tier: o.Usage.Tier, Currency: "EUR", Source: "operator override", AsOf: "2026-11-01", Input: moneyPtr(1000000), Output: moneyPtr(2000000)}
	m := finishRequestMoney(offlineMoneyInput(route, o), route, &o, []MoneyRate{card})
	if m.APIEquivalent.Amount != nil {
		t.Fatal("bundled cache rates filled a hole in explicit card", m.APIEquivalent)
	}
	card.CacheRead = moneyPtr(300000)
	m = finishRequestMoney(offlineMoneyInput(route, o), route, &o, []MoneyRate{card})
	if e := m.APIEquivalent; e.Amount == nil || *e.Amount != 12700 || e.Currency != "EUR" || e.Source != card.Source {
		t.Fatal("explicit card lost precedence", e)
	}
	// Anthropic cache-write TTL remains unknown even for an OAuth plan.
	route.Upstream = "https://api.anthropic.com"
	o = ObservedUsage{Protocol: usagetap.Messages, Usage: usagetap.Usage{Model: "claude-opus-5", Tier: "standard", Input: 1000, Output: 200, CachedInput: 100, CacheWrite: 100, Source: usagetap.Reported, Turns: 1}}
	m = finishRequestMoney(offlineMoneyInput(route, o), route, &o, nil)
	if m.APIEquivalent.Amount != nil || !strings.Contains(m.APIEquivalent.Reason, "TTL") {
		t.Fatal("plan cache-write TTL invented", m.APIEquivalent)
	}
	// Long-context thresholds include cache reads and freeze whole-request rates.
	route.Upstream = "https://chatgpt.com/backend-api/codex"
	o = offlineObserved("gpt-5.6-terra", "default", 272001, 72001, 100)
	m = finishRequestMoney(offlineMoneyInput(route, o), route, &o, nil)
	if e := m.APIEquivalent; e.Amount == nil || *e.Amount != 830601 || !strings.Contains(e.Source, "whole-request") || *e.Rate.Output != 18000000 {
		t.Fatal("plan long-context comparison incorrect", e)
	}
}

func TestMoneyPlanAPIEquivalentSummarySeparateFromSpending(t *testing.T) {
	now := time.Now()
	rate := testMoneyRate()
	rate.Tier = "standard"
	in := MoneyInput{Billing: "plan", Provider: rate.Provider, Model: rate.Model, Tier: rate.Tier, UsageState: "reported", InputTokens: 1000}
	plan := CalculateMoney(in, []MoneyRate{rate})
	partial := plan
	partial.Kind = "unmeasured"
	floor := *plan.APIEquivalent
	floor.Kind = "estimated_floor"
	partial.APIEquivalent = &floor
	charged := Money{Billing: "pay_as_you_go", Kind: "charged", Amount: moneyPtr(123), Currency: "USD", Source: "vendor", AsOf: "2026-11-16", APIEquivalent: plan.APIEquivalent}
	rows := []MoneyRecord{{Started: now, Money: plan}, {Started: now, Money: partial}, {Started: now, Money: Money{Billing: "plan", Kind: "included"}}, {Started: now, Money: charged}}
	settings := MoneySettings{Budget: &MoneyBudget{Currency: "USD", LimitMicros: 10000}}
	summary, err := BuildMoneySummary(now, settings, rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []MoneyPeriod{summary.Month, summary.Today, summary.Devices[0].Period, summary.Apps[0].Period, summary.Providers[0].Period} {
		if len(p.Totals) != 1 || p.Totals[0].Total != 123 || p.Totals[0].Charged != 123 || p.Totals[0].Estimated != 0 || p.APIEquivalentCovered != 2 || p.APIEquivalentUnpriced != 1 {
			t.Fatalf("mixed actual/hypothetical accounting: %+v", p)
		}
		if len(p.APIEquivalentTotals) != 1 || p.APIEquivalentTotals[0].Total != 4000 || p.APIEquivalentTotals[0].Charged != 0 || p.APIEquivalentTotals[0].Estimated != 2000 || p.APIEquivalentTotals[0].Floor != 2000 || p.APIEquivalentTotals[0].Qualifier != "at_least" {
			t.Fatalf("incorrect separate plan totals: %+v", p)
		}
	}
	withoutEquivalent := append([]MoneyRecord(nil), rows...)
	for i := range withoutEquivalent {
		withoutEquivalent[i].Money.APIEquivalent = nil
	}
	baseline, err := BuildMoneySummary(now, settings, withoutEquivalent)
	if err != nil || !reflect.DeepEqual(summary.Budget, baseline.Budget) || !reflect.DeepEqual(summary.Month.Totals, baseline.Month.Totals) {
		t.Fatalf("comparison changed spending/budget: %+v vs %+v, %v", summary, baseline, err)
	}
}

func TestMoneyPlanAPIEquivalentHistoryFrozenWithoutRepricing(t *testing.T) {
	s := testService(t)
	route := Route{Name: "family", Billing: "plan", Upstream: "https://api.anthropic.com", Account: "claude"}
	o := ObservedUsage{Protocol: usagetap.Messages, Usage: usagetap.Usage{Model: "claude-opus-5", Tier: "standard", Input: 1000, Output: 200, CachedInput: 100, CacheWrite: 0, Source: usagetap.Reported, Turns: 1}}
	now := time.Now()
	rec := Record{ID: "frozen-plan-api-equivalent", Started: now.UnixMilli(), Outcome: "unknown", Route: route.Name}
	if err := s.store.start(context.Background(), &rec); err != nil {
		t.Fatal(err)
	}
	rec.Outcome, rec.Usage = "complete", &o
	s.finalizeMoney(&rec, route)
	if err := s.store.finish(rec); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(original), &fields); err != nil {
		t.Fatal(err)
	}
	var moneyFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["Money"], &moneyFields); err != nil || moneyFields["api_equivalent"] == nil || moneyFields["amount_micros"] != nil {
		t.Fatalf("incorrect persisted JSON contract: %s, %v", original, err)
	}
	// Change today's rates, delete technical history, then summarize durable rows.
	settings, _ := json.Marshal(MoneySettings{Rates: []MoneyRate{{Provider: route.Name, Model: o.Usage.Model, Tier: o.Usage.Tier, Currency: "EUR", Source: "later price", AsOf: "2026-11-16", Input: moneyPtr(99000000), Output: moneyPtr(99000000)}}})
	if _, err := s.store.db.Exec("UPDATE money_settings SET data=? WHERE id=1", string(settings)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("DELETE FROM requests"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.reconcileMoneyHistory(context.Background(), 0, now.Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", rec.ID).Scan(&after); err != nil || after != original {
		t.Fatalf("historical JSON was rewritten: %v\n%s\n%s", err, original, after)
	}
	var row MoneyRecord
	if err := json.Unmarshal([]byte(after), &row); err != nil {
		t.Fatal(err)
	}
	e := row.Money.APIEquivalent
	if e == nil || e.Model != "claude-opus-5" || e.Amount == nil || *e.Amount != 10050 || e.AsOf != "2026-09-14" || e.Rate == nil || *e.Rate.Input != 5000000 || e.Rate.CacheWrite != nil {
		t.Fatalf("public comparison not frozen: %+v", e)
	}
	loaded, err := s.store.loadMoneySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := BuildMoneySummary(now.Add(time.Second), loaded, []MoneyRecord{row})
	if err != nil || len(summary.Month.Totals) != 0 || len(summary.Month.APIEquivalentTotals) != 1 || summary.Month.APIEquivalentTotals[0].Total != 10050 || summary.Month.APIEquivalentTotals[0].Currency != "USD" {
		t.Fatalf("history repriced or counted as spending: %+v, %v", summary, err)
	}
	// Old snapshots remain byte-compatible and do not gain today's API prices.
	old := `{"billing":"plan","kind":"included"}`
	var legacy Money
	if err := json.Unmarshal([]byte(old), &legacy); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil || string(encoded) != old || legacy.APIEquivalent != nil {
		t.Fatalf("legacy money changed: %s, %v", encoded, err)
	}
}
