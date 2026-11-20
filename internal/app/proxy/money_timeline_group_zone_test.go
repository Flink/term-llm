package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func TestTimelineAPIByModel(t *testing.T) {
	s := testService(t)
	now := time.Now().Add(-time.Minute)
	usage := func(model string, ambiguous bool) *ObservedUsage {
		return &ObservedUsage{Protocol: usagetap.Responses, Usage: usagetap.Usage{
			Model: model, Ambiguous: ambiguous, Input: 10, Output: 2, Source: usagetap.Reported,
		}}
	}
	paid := Money{Billing: "pay_as_you_go", Kind: "charged", Amount: moneyPtr(80), Currency: "USD", Source: "fixture", AsOf: "2026-11-17"}
	plan := timelinePlanEstimate("estimated", "USD", 30)
	plan.APIEquivalent.Model = "test-model"
	unknown := Money{Billing: "pay_as_you_go", Kind: "unmeasured"}
	rows := []struct {
		id  string
		rec MoneyRecord
	}{
		{"paid", MoneyRecord{Provider: "provider-a", Usage: usage("test-model", false), Money: paid}},
		{"plan", MoneyRecord{Provider: "provider-b", Money: plan}},
		{"total", MoneyRecord{Usage: usage("total", false), Money: paid}},
		{"missing", MoneyRecord{Money: unknown}},
		{"ambiguous", MoneyRecord{Usage: usage("test-model", true), Money: unknown}},
	}
	for _, row := range rows {
		insertTimelineEvidence(t, s, row.id, now, row.rec)
	}
	for i := range 5 {
		model := fmt.Sprintf("cheap-%d", i)
		row := timelineCharge("USD", 1)
		row.Usage = usage(model, false)
		for j := range 3 {
			insertTimelineEvidence(t, s, fmt.Sprintf("%s-%d", model, j), now, row)
		}
	}
	for model, micros := range map[string]int64{"premium": 9000, "Other": 200} {
		row := timelineCharge("EUR", micros)
		row.Usage = usage(model, false)
		insertTimelineEvidence(t, s, model, now, row)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/_proxy/api/timeline?range=7d&group=model&timezone=Australia%2FSydney", nil)
	if !s.moneyAPI(w, r, "timeline") || w.Code != http.StatusOK {
		t.Fatalf("timeline status %d: %s", w.Code, w.Body.String())
	}
	var got moneytimeline.Response
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Timezone != "Australia/Sydney" {
		t.Fatalf("wrong timezone: %q", got.Timezone)
	}
	want := map[string]moneytimeline.Series{
		"test-model": {Key: "model:test-model", Label: "test-model", Tokens: moneyPtr(12), TokenParts: &moneytimeline.TokenParts{Input: 10, Output: 2}, UnknownTokens: 1, Plan: 1, Costs: []moneytimeline.Cost{{Currency: "USD", Micros: 110, Estimated: true, PlanEstimateMicros: moneyPtr(30)}}},
		"total":      {Key: "model:total", Label: "total", Tokens: moneyPtr(12), TokenParts: &moneytimeline.TokenParts{Input: 10, Output: 2}, Costs: []moneytimeline.Cost{{Currency: "USD", Micros: 80}}},
		"Unknown model": {Key: "model:", Label: "Unknown model", Tokens: moneyPtr(12), TokenParts: &moneytimeline.TokenParts{Input: 10, Output: 2},
			UnknownCost: 2, UnknownTokens: 1, Costs: []moneytimeline.Cost{}},
	}
	for i := range 5 {
		model := fmt.Sprintf("cheap-%d", i)
		want[model] = moneytimeline.Series{Key: "model:" + model, Label: model, Tokens: moneyPtr(36),
			TokenParts: &moneytimeline.TokenParts{Input: 30, Output: 6}, Costs: []moneytimeline.Cost{{Currency: "USD", Micros: 3}}}
	}
	for model, micros := range map[string]int64{"premium": 9000, "Other": 200} {
		want[model] = moneytimeline.Series{Key: "model:" + model, Label: model, Tokens: moneyPtr(12),
			TokenParts: &moneytimeline.TokenParts{Input: 10, Output: 2}, Costs: []moneytimeline.Cost{{Currency: "EUR", Micros: micros}}}
	}
	if !reflect.DeepEqual(got.Currencies, []string{"EUR", "USD"}) {
		t.Fatalf("unexpected currencies: %v", got.Currencies)
	}
	if !strings.Contains(w.Body.String(), `"token_parts":{"input":10,"output":2,"cache_read":0,"cache_write":0}`) {
		t.Fatalf("missing normalized token parts on the wire: %s", w.Body.String())
	}
	byModel := map[string]moneytimeline.Series{}
	for _, bucket := range got.Buckets {
		for _, item := range bucket.Series {
			if _, ok := byModel[item.Label]; ok {
				t.Fatalf("duplicate model label %q in fixture: %s", item.Label, w.Body.String())
			}
			byModel[item.Label] = item
		}
	}
	if !reflect.DeepEqual(byModel, want) {
		t.Fatalf("unexpected model breakdown: %s", w.Body.String())
	}
}

func TestTimelineTimezoneChangesBucketsNotMoneySettings(t *testing.T) {
	s := testService(t)
	setTimelineZone(t, s, "UTC")
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 11, 17, 18, 30, 0, 0, time.UTC) // Nov 18 in Kolkata
	now := midnight.Add(90 * time.Minute)
	for id, at := range map[string]time.Time{"previous": midnight.Add(-time.Millisecond), "current": midnight} {
		insertTimelineEvidence(t, s, id, at, MoneyRecord{Money: Money{Billing: "pay_as_you_go", Kind: "unmeasured"}})
	}
	ctx := context.Background()
	today, err := s.moneyTimeline(ctx, now, "today", "total", loc)
	if err != nil {
		t.Fatal(err)
	}
	if !today.Start.Equal(midnight) || len(today.Buckets) != 2 || today.Buckets[0].Label != "2026-11-18 00:00 +0530" ||
		len(today.Buckets[0].Series) != 1 || today.Buckets[0].Series[0].UnknownCost != 1 || len(today.Buckets[1].Series) != 0 {
		t.Fatalf("wrong hourly bins in browser timezone: %+v", today)
	}
	week, err := s.moneyTimeline(ctx, now, "7d", "total", loc)
	if err != nil {
		t.Fatal(err)
	}
	previous, current := week.Buckets[5], week.Buckets[6]
	if previous.Label != "2026-11-17" || current.Label != "2026-11-18" || !current.Start.Equal(midnight) ||
		len(previous.Series) != 1 || previous.Series[0].UnknownCost != 1 || len(current.Series) != 1 || current.Series[0].UnknownCost != 1 {
		t.Fatalf("wrong daily bins in browser timezone: %+v", week)
	}
	fallback, err := s.moneyTimeline(ctx, now, "today", "total", nil)
	if err != nil || fallback.Timezone != "UTC" || fallback.Start.Hour() != 0 || fallback.Start.Day() != 17 {
		t.Fatalf("configured timezone fallback changed: %+v, %v", fallback, err)
	}
	summary, err := s.moneySummary(ctx, now)
	if err != nil || summary.Timezone != "UTC" || summary.Today.Requests != 2 {
		t.Fatalf("chart timezone changed money summary: %+v, %v", summary, err)
	}
}
