package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func timelinePlanEstimate(kind, currency string, micros int64) Money {
	return Money{Billing: "plan", Kind: "included", APIEquivalent: &APIEquivalent{
		Kind: kind, Currency: currency, Amount: moneyPtr(micros), Source: "fixture", AsOf: "2026-11-16",
	}}
}

func TestTimelineStoredPlanOnlyOnDefaultAPI(t *testing.T) {
	s := testService(t)
	setTimelineZone(t, s, "UTC")
	insertTimelineEvidence(t, s, "plan", time.Now().Add(-time.Minute), MoneyRecord{Money: timelinePlanEstimate("estimated", "USD", 40)})
	w := httptest.NewRecorder()
	if !s.moneyAPI(w, httptest.NewRequest(http.MethodGet, "/_proxy/api/timeline", nil), "timeline") || w.Code != http.StatusOK {
		t.Fatalf("default timeline status %d: %s", w.Code, w.Body.String())
	}
	var got moneyTimeline
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Range != "30d" || !reflect.DeepEqual(got.Currencies, []string{"USD"}) {
		t.Fatalf("unexpected default range or currencies: %s", w.Body.String())
	}
	var all []timelineSeries
	for _, bucket := range got.Buckets {
		all = append(all, bucket.Series...)
	}
	if len(all) != 1 || all[0].Key != "total" || all[0].UnknownCost != 0 || all[0].Plan != 1 {
		t.Fatalf("unexpected default series: %+v", all)
	}
	want := []timelineCost{{Currency: "USD", Micros: 40, Estimated: true, PlanEstimateMicros: moneyPtr(40)}}
	if !reflect.DeepEqual(all[0].Costs, want) || !strings.Contains(w.Body.String(), `"plan_estimate_micros":40`) {
		t.Fatalf("default timeline did not include the frozen estimate: %s", w.Body.String())
	}
}

func TestTimelineStoredPlanMixesCostsWithoutChangingSummaryOrBudget(t *testing.T) {
	s := testService(t)
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	settings, err := json.Marshal(MoneySettings{Timezone: "UTC", Budget: &MoneyBudget{Currency: "USD", LimitMicros: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("UPDATE money_settings SET data=? WHERE id=1", string(settings)); err != nil {
		t.Fatal(err)
	}
	for id, row := range map[string]Money{
		"plan-usd": timelinePlanEstimate("estimated_floor", "USD", 40),
		"plan-eur": timelinePlanEstimate("estimated", "EUR", 30),
		"legacy":   {Billing: "plan", Kind: "included"},
		"paid-usd": {Billing: "pay_as_you_go", Kind: "charged", Currency: "USD", Amount: moneyPtr(100), Source: "fixture", AsOf: "2026-11-16"},
		"paid-aud": {Billing: "pay_as_you_go", Kind: "estimated", Currency: "AUD", Amount: moneyPtr(20), Source: "fixture", AsOf: "2026-11-16"},
	} {
		insertTimelineEvidence(t, s, id, now, MoneyRecord{Money: row})
	}
	ctx := context.Background()
	before, err := s.moneySummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.moneyTimeline(ctx, now, "today", "total", nil)
	if err != nil {
		t.Fatal(err)
	}
	series := got.Buckets[len(got.Buckets)-1].Series
	want := []timelineCost{
		{Currency: "AUD", Micros: 20, Estimated: true},
		{Currency: "EUR", Micros: 30, Estimated: true, PlanEstimateMicros: moneyPtr(30)},
		{Currency: "USD", Micros: 140, Estimated: true, PlanEstimateMicros: moneyPtr(40)},
	}
	if len(series) != 1 || series[0].UnknownCost != 2 || series[0].Plan != 3 || !reflect.DeepEqual(series[0].Costs, want) {
		t.Fatalf("wrong mixed cost or coverage: %+v", series)
	}
	actualOnly, err := json.Marshal(series[0].Costs[0])
	if err != nil || strings.Contains(string(actualOnly), "plan_estimate_micros") {
		t.Fatalf("non-plan currency has a plan field: %s, err=%v", actualOnly, err)
	}
	after, err := s.moneySummary(ctx, now)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reading the timeline changed the summary: before=%+v after=%+v err=%v", before, after, err)
	}
	if len(after.Month.Totals) != 2 || after.Month.Totals[1].Currency != "USD" || after.Month.Totals[1].Charged != 100 || after.Month.Totals[1].Total != 100 ||
		after.Budget == nil || after.Budget.RemainingMicros != 900 || after.Budget.UsedPercent != 10 {
		t.Fatalf("plan estimates leaked into actual spend or budget: %+v, budget=%+v", after.Month, after.Budget)
	}
}

func TestTimelineStoredPlanCoverageRequiresValidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*Money)
		unknown  int64
		wantCost bool
	}{
		{"zero complete", func(m *Money) { m.APIEquivalent.Amount = moneyPtr(0) }, 0, true},
		{"positive floor", func(m *Money) { m.APIEquivalent.Kind = "estimated_floor" }, 1, true},
		{"zero floor", func(m *Money) { m.APIEquivalent.Kind = "estimated_floor"; m.APIEquivalent.Amount = moneyPtr(0) }, 1, false},
		{"legacy", func(m *Money) { m.APIEquivalent = nil }, 1, false},
		{"unpriced", func(m *Money) { m.APIEquivalent.Kind = "unpriced"; m.APIEquivalent.Amount = nil }, 1, false},
		{"unmeasured", func(m *Money) { m.APIEquivalent.Kind = "unmeasured"; m.APIEquivalent.Amount = nil }, 1, false},
		{"charged equivalent", func(m *Money) { m.APIEquivalent.Kind = "charged" }, 1, false},
		{"missing amount", func(m *Money) { m.APIEquivalent.Amount = nil }, 1, false},
		{"negative amount", func(m *Money) { m.APIEquivalent.Amount = moneyPtr(-1) }, 1, false},
		{"oversize amount", func(m *Money) { m.APIEquivalent.Amount = moneyPtr(maxMoneyMicros + 1) }, 1, false},
		{"invalid currency", func(m *Money) { m.APIEquivalent.Currency = "usd" }, 1, false},
		{"missing source", func(m *Money) { m.APIEquivalent.Source = "" }, 1, false},
		{"invalid date", func(m *Money) { m.APIEquivalent.AsOf = "2026-11-31" }, 1, false},
		{"blocked before dispatch", func(m *Money) { m.Kind = "no_charge_expected"; m.APIEquivalent = nil }, 0, false},
		{"local billing", func(m *Money) { m.Billing = "local" }, 0, false},
		{"non-plan unpriced", func(m *Money) { m.Billing = "pay_as_you_go"; m.Kind = "unpriced" }, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := timelinePlanEstimate("estimated", "USD", 15)
			tc.change(&m)
			a, currencies := freshTimelineAggregate(), map[string]bool{}
			accumulateTimelineRecord(a, MoneyRecord{Money: m}, currencies)
			series := timelineSeriesFromAggregate("total", a, []string{"USD"})
			var want []timelineCost
			if tc.wantCost {
				want = []timelineCost{{Currency: "USD", Micros: *m.APIEquivalent.Amount, Estimated: true, PlanEstimateMicros: m.APIEquivalent.Amount}}
			}
			if series.UnknownCost != tc.unknown || currencies["USD"] != tc.wantCost || (tc.wantCost && !reflect.DeepEqual(series.Costs, want)) || (!tc.wantCost && len(series.Costs) != 0) {
				t.Fatalf("wrong cost or coverage: %+v, currencies=%v", series, currencies)
			}
		})
	}
}

func TestTimelineStoredPlanGroupsByClientAndProviderOnLocalDates(t *testing.T) {
	s := testService(t)
	setTimelineZone(t, s, "Australia/Sydney")
	midnight := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC) // Nov 18 local
	now := midnight.Add(time.Hour)
	insertTimelineEvidence(t, s, "previous-day", midnight.Add(-time.Millisecond), MoneyRecord{
		DeviceID: "a", Provider: "chatgpt", Money: timelinePlanEstimate("estimated", "USD", 20),
	})
	for _, device := range []string{"a", "b", "c", "d", "e"} {
		insertTimelineEvidence(t, s, device, midnight, MoneyRecord{DeviceID: device, Provider: "fixture", Money: Money{Billing: "local", Kind: "none"}})
	}
	insertTimelineEvidence(t, s, "f", midnight, MoneyRecord{DeviceID: "f", Provider: "openai", Money: timelinePlanEstimate("estimated", "USD", 30)})
	insertTimelineEvidence(t, s, "g", midnight, MoneyRecord{DeviceID: "g", Provider: "openai", Money: timelinePlanEstimate("estimated_floor", "USD", 40)})

	for _, tc := range []struct {
		group, previousKey, currentKey string
		seriesCount                    int
	}{
		{"client", "a", timelineOtherKey, 6},
		{"provider", "chatgpt", "openai", 2},
	} {
		t.Run(tc.group, func(t *testing.T) {
			got, err := s.moneyTimeline(context.Background(), now, "7d", tc.group, nil)
			if err != nil {
				t.Fatal(err)
			}
			before, after := got.Buckets[5], got.Buckets[6]
			if before.Label != "2026-11-17" || after.Label != "2026-11-18" || !after.Start.Equal(midnight) ||
				len(before.Series) != 1 || len(after.Series) != tc.seriesCount {
				t.Fatalf("unexpected buckets: before=%+v after=%+v", before, after)
			}
			wantDay1 := []timelineCost{{Currency: "USD", Micros: 20, Estimated: true, PlanEstimateMicros: moneyPtr(20)}}
			if day1 := before.Series[0]; day1.Key != tc.previousKey || day1.Plan != 1 || day1.UnknownCost != 0 || !reflect.DeepEqual(day1.Costs, wantDay1) {
				t.Fatalf("previous day's estimate went into the wrong group: %+v", day1)
			}
			wantDay2 := []timelineCost{{Currency: "USD", Micros: 70, Estimated: true, PlanEstimateMicros: moneyPtr(70)}}
			var found bool
			for _, series := range after.Series {
				if series.Key == tc.currentKey {
					found = true
					if series.Plan != 2 || series.UnknownCost != 1 || !reflect.DeepEqual(series.Costs, wantDay2) {
						t.Fatalf("group lost plan totals or floor qualifier: %+v", series)
					}
				} else if len(series.Costs) > 0 {
					t.Fatalf("estimate assigned to wrong group: %+v", series)
				}
			}
			if !found {
				t.Fatalf("missing group %q: %+v", tc.currentKey, after.Series)
			}
		})
	}
}

func TestTimelineStoredPlanOverflowPreservesExistingCost(t *testing.T) {
	a, b := freshTimelineAggregate(), freshTimelineAggregate()
	currencies := map[string]bool{}
	for _, row := range []Money{timelinePlanEstimate("estimated", "USD", maxMoneyMicros), timelinePlanEstimate("estimated", "USD", 30)} {
		accumulateTimelineRecord(a, MoneyRecord{Money: row}, currencies)
	}
	accumulateTimelineRecord(b, MoneyRecord{Money: timelinePlanEstimate("estimated_floor", "USD", 40)}, currencies)
	mergeTimelineAggregate(a, b)
	before := timelineSeriesFromAggregate("total", a, []string{"USD"})
	wantPlan := []timelineCost{{Currency: "USD", Micros: maxMoneyMicros, Estimated: true, PlanEstimateMicros: moneyPtr(maxMoneyMicros)}}
	if before.UnknownCost != 2 || !reflect.DeepEqual(before.Costs, wantPlan) {
		t.Fatalf("overflow dropped plotted plan estimate or counted a floor twice: %+v", before)
	}
	paid := Money{Billing: "pay_as_you_go", Kind: "charged", Currency: "USD", Amount: moneyPtr(100), Source: "fixture", AsOf: "2026-11-16"}
	accumulateTimelineRecord(a, MoneyRecord{Money: paid}, currencies)
	after := timelineSeriesFromAggregate("total", a, []string{"USD"})
	if after.UnknownCost != 3 || !reflect.DeepEqual(after.Costs, []timelineCost{{Currency: "USD", Micros: 100}}) {
		t.Fatalf("mixed currency overflow lost the existing plotted charge: %+v", after)
	}
}
