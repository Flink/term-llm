package proxy

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMoneyPlanAPIEquivalentSummaryRejectsInvalidEvidence(t *testing.T) {
	now := time.Now()
	base := APIEquivalent{Kind: "estimated", Amount: moneyPtr(1), Currency: "USD", Source: "operator", AsOf: "2026-11-16"}
	for _, tt := range []struct {
		name   string
		change func(*APIEquivalent)
	}{
		{"missing amount", func(e *APIEquivalent) { e.Amount = nil }},
		{"negative amount", func(e *APIEquivalent) { e.Amount = moneyPtr(-1) }},
		{"out of range", func(e *APIEquivalent) { e.Amount = moneyPtr(maxMoneyMicros + 1) }},
		{"undated", func(e *APIEquivalent) { e.AsOf = "" }},
		{"currency unknown", func(e *APIEquivalent) { e.Currency = "" }},
		{"source missing", func(e *APIEquivalent) { e.Source = "" }},
		{"charge not hypothetical", func(e *APIEquivalent) { e.Kind = "charged" }},
		{"zero partial", func(e *APIEquivalent) { e.Kind = "estimated_floor"; e.Amount = moneyPtr(0) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := base
			tt.change(&e)
			s, err := BuildMoneySummary(now, MoneySettings{}, []MoneyRecord{{Started: now, Money: Money{Billing: "plan", Kind: "included", APIEquivalent: &e}}})
			if err != nil || s.Month.APIEquivalentCovered != 0 || s.Month.APIEquivalentUnpriced != 1 || len(s.Month.APIEquivalentTotals) != 0 || len(s.Month.Totals) != 0 {
				t.Fatalf("invalid comparison counted: %+v, %v", s.Month, err)
			}
		})
	}
	max := base
	max.Amount = moneyPtr(maxMoneyMicros)
	rows := []MoneyRecord{
		{Started: now, Money: Money{Billing: "plan", Kind: "included", APIEquivalent: &max}},
		{Started: now, Money: Money{Billing: "plan", Kind: "included", APIEquivalent: &base}},
		{Started: now, Money: Money{Billing: "plan", Kind: "no_charge_expected"}},
	}
	s, err := BuildMoneySummary(now, MoneySettings{}, rows)
	if err != nil || s.Month.APIEquivalentCovered != 1 || s.Month.APIEquivalentUnpriced != 1 || len(s.Month.APIEquivalentTotals) != 1 || s.Month.APIEquivalentTotals[0].Total != maxMoneyMicros || s.Month.APIEquivalentTotals[0].Qualifier != "at_least" || s.Month.NoChargeExpected != 1 {
		t.Fatalf("overflow lost known comparison or counted blocked request: %+v, %v", s.Month, err)
	}
	// Known complete zero remains an estimate, not a charge or missing coverage.
	zero := base
	zero.Amount = moneyPtr(0)
	rows[0].Money.APIEquivalent = &zero
	s, err = BuildMoneySummary(now, MoneySettings{}, rows[:1])
	if err != nil || s.Month.APIEquivalentCovered != 1 || s.Month.APIEquivalentUnpriced != 0 || s.Month.APIEquivalentTotals[0].Qualifier != "about" {
		t.Fatalf("known zero comparison mislabeled: %+v, %v", s.Month, err)
	}
	data, err := json.Marshal(emptyMoneyPeriod())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields["api_equivalent_totals"] != nil || string(fields["api_equivalent_covered"]) != "0" || string(fields["api_equivalent_unpriced"]) != "0" {
		t.Fatalf("unexpected period JSON contract: %s, %v", data, err)
	}
}

func TestMoneyPlanAPIEquivalentDisplayedSeparatelyFromTimelineCharges(t *testing.T) {
	plan := CalculateMoney(MoneyInput{Billing: "plan", Provider: "fixture", Model: "exact", Tier: "standard", UsageState: "reported", InputTokens: 1000}, []MoneyRate{{Provider: "fixture", Model: "exact", Tier: "standard", Input: moneyPtr(2000000), Output: moneyPtr(6000000), Currency: "USD", Source: "operator", AsOf: "2026-11-16"}})
	if plan.APIEquivalent == nil || plan.APIEquivalent.Amount == nil {
		t.Fatal("test did not price plan")
	}
	a := &timelineAggregate{cost: map[string]int64{}, estimated: map[string]bool{}}
	currencies := map[string]bool{}
	accumulateTimelineRecord(a, MoneyRecord{Money: plan}, currencies)
	series := timelineSeriesFromAggregate("total", a, []string{"USD"})
	if len(a.cost) != 0 || a.plan != 1 || !currencies["USD"] || len(series.Costs) != 1 {
		t.Fatalf("plan estimate must be charted without recording a pay-as-you-go charge: %+v", series)
	}
	cost := series.Costs[0]
	if !cost.Estimated || cost.PlanEstimateMicros == nil || *cost.PlanEstimateMicros != *plan.APIEquivalent.Amount || cost.Micros != *plan.APIEquivalent.Amount {
		t.Fatalf("plan estimate not identified in chart data: %+v", cost)
	}
}
