package proxy

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func timelineCharge(currency string, micros int64) MoneyRecord {
	return MoneyRecord{Money: Money{Billing: "pay_as_you_go", Kind: "charged", Amount: moneyPtr(micros), Currency: currency, Source: "fixture", AsOf: "2026-11-17"}}
}

func TestTimelinePreviousRequiresCoveringHistory(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		oldest  time.Time
		covered bool
	}{
		{name: "no history"},
		{name: "partial history", oldest: start.Add(time.Millisecond)},
		{name: "covered without rows in window", oldest: start.Add(-time.Millisecond), covered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			if !tc.oldest.IsZero() {
				insertTimelineEvidence(t, s, "oldest", tc.oldest, timelineCharge("USD", 50))
			}
			got, err := s.moneyTimeline(context.Background(), now, "7d", "total", time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			var want *moneytimeline.Series
			if tc.covered {
				want = &moneytimeline.Series{Key: "total", Label: "Total", Costs: []moneytimeline.Cost{}}
			}
			if !reflect.DeepEqual(got.Previous, want) {
				t.Fatalf("previous = %+v, want %+v", got.Previous, want)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if _, ok := wire["previous"]; ok != tc.covered {
				t.Fatalf("wrong previous field presence: %s", data)
			}
		})
	}
}

func TestTimelinePreviousIsUngroupedAndDoesNotOverlapAtFallBack(t *testing.T) {
	s := testService(t)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, loc)
	priorStart := start.AddDate(0, 0, -1)
	insertTimelineEvidence(t, s, "prior-start", priorStart, timelineCharge("USD", 50))
	insertTimelineEvidence(t, s, "prior-last", start.Add(-time.Millisecond), MoneyRecord{
		Money: timelinePlanEstimate("estimated_floor", "USD", 30),
		Usage: &ObservedUsage{Protocol: usagetap.Responses, Usage: usagetap.Usage{Input: 10, Output: 2, Source: usagetap.Reported}},
	})
	// Malformed frozen evidence contributes unknowns, not a charge or zero tokens.
	if _, err := s.store.db.Exec("INSERT INTO money_history(request_id,started,caller,route,data) VALUES(?,?,?,?,?)", "corrupt", priorStart.Add(time.Hour).UnixMilli(), "client", "route", "{"); err != nil {
		t.Fatal(err)
	}
	insertTimelineEvidence(t, s, "current-start", start, timelineCharge("AUD", 200))
	insertTimelineEvidence(t, s, "current-end", now, timelineCharge("AUD", 300))
	insertTimelineEvidence(t, s, "future", now.Add(time.Millisecond), timelineCharge("GBP", 9000))

	want := &moneytimeline.Series{Key: "total", Label: "Total", Tokens: moneyPtr(12), TokenParts: &moneytimeline.TokenParts{Input: 10, Output: 2}, UnknownCost: 2, UnknownTokens: 2, Plan: 1,
		Costs: []moneytimeline.Cost{{Currency: "USD", Micros: 80, Estimated: true, PlanEstimateMicros: moneyPtr(30)}}}
	for _, group := range []string{"total", "client", "provider", "model"} {
		t.Run(group, func(t *testing.T) {
			got, err := s.moneyTimeline(context.Background(), now, "today", group, loc)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Previous, want) {
				t.Fatalf("wrong previous totals: %+v, want %+v", got.Previous, want)
			}
			if !reflect.DeepEqual(got.Currencies, []string{"AUD"}) {
				t.Fatalf("prior-only or future currencies in current chart: %v", got.Currencies)
			}
			for i, bucket := range got.Buckets {
				var cost int64
				for _, series := range bucket.Series {
					for _, c := range series.Costs {
						cost += c.Micros
					}
				}
				var wantCost int64
				switch i {
				case 0:
					wantCost = 200
				case len(got.Buckets) - 1:
					wantCost = 300
				}
				if cost != wantCost {
					t.Fatalf("current bucket %s cost = %d, want %d", bucket.Label, cost, wantCost)
				}
			}
		})
	}
}

func TestTimelinePreviousExcludesRowsAtOrAfterElapsedEnd(t *testing.T) {
	s := testService(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	prevEnd := now.AddDate(0, 0, -7)
	insertTimelineEvidence(t, s, "retained", prevEnd.AddDate(0, 0, -8), timelineCharge("USD", 9000))
	insertTimelineEvidence(t, s, "before", prevEnd.Add(-time.Millisecond), timelineCharge("USD", 1))
	insertTimelineEvidence(t, s, "at-end", prevEnd, timelineCharge("USD", 10))
	insertTimelineEvidence(t, s, "after", prevEnd.Add(time.Millisecond), timelineCharge("USD", 9000))
	for _, tc := range []struct {
		elapsed time.Duration
		want    int64
	}{{0, 1}, {time.Nanosecond, 11}} {
		got, err := s.moneyTimeline(context.Background(), now.Add(tc.elapsed), "7d", "total", time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		want := []moneytimeline.Cost{{Currency: "USD", Micros: tc.want}}
		if got.Previous == nil || !reflect.DeepEqual(got.Previous.Costs, want) {
			t.Fatalf("elapsed end + %v: previous = %+v, want costs %v", tc.elapsed, got.Previous, want)
		}
	}
}
