package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

// Keep the chart cost fixture shared with the provider-level usage tests.
type timelineCost = moneytimeline.Cost

func setTimelineZone(t *testing.T, s *Service, zone string) {
	t.Helper()
	raw, _ := json.Marshal(MoneySettings{Timezone: zone})
	if _, err := s.store.db.Exec("UPDATE money_settings SET data=? WHERE id=1", string(raw)); err != nil {
		t.Fatal(err)
	}
}
func TestMoneyTimelineBinsUseLocalCalendar(t *testing.T) {
	s := testService(t)
	setTimelineZone(t, s, "Australia/Sydney")
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	got, err := s.moneyTimeline(context.Background(), now, "7d", "total", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 7 || got.Timezone != "Australia/Sydney" || got.Buckets[0].Start.In(time.FixedZone("", 11*3600)).Day() != 11 {
		t.Fatalf("unexpected bounds: %+v", got)
	}
	if got.Buckets[6].End.After(now) || got.Buckets[6].End.Before(got.Buckets[6].Start) {
		t.Fatalf("invalid/future bucket: %+v", got.Buckets[6])
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if json.Unmarshal(data, &wire) != nil || wire["buckets"] == nil {
		t.Fatal("invalid timeline schema")
	}
}
func TestMoneyTimelineTodayDSTRepeatHasDistinctOffset(t *testing.T) {
	s := testService(t)
	setTimelineZone(t, s, "America/New_York")
	now := time.Date(2026, 11, 2, 4, 59, 0, 0, time.UTC)
	got, err := s.moneyTimeline(context.Background(), now, "today", "total", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 25 {
		t.Fatalf("fall-back bins=%d", len(got.Buckets))
	}
	labels := map[string]bool{}
	for _, b := range got.Buckets {
		labels[b.Label] = true
		if b.End.Before(b.Start) {
			t.Fatalf("inverted bucket: %+v", b)
		}
	}
	if len(labels) != 25 {
		t.Fatalf("repeated hour labels collide: %v", labels)
	}
}
func TestMoneyTimelineKeepsKnownTokensBesideUnknownEvidence(t *testing.T) {
	s := testService(t)
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	rows := []struct {
		id    string
		usage *ObservedUsage
	}{{"known", &ObservedUsage{Protocol: usagetap.Responses, Usage: usagetap.Usage{Input: 10, CachedInput: 2, Output: 4, Reasoning: 1, Source: usagetap.Reported}}}, {"partial", &ObservedUsage{Protocol: usagetap.Messages, Usage: usagetap.Usage{Input: 20, Output: -1, Source: usagetap.Partial}}}, {"legacy", nil}}
	for _, row := range rows {
		data, err := json.Marshal(MoneyRecord{Started: now, Provider: "p", Money: Money{Billing: "pay_as_you_go", Kind: "unmeasured"}, Usage: row.usage})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.store.db.Exec("INSERT INTO money_history(request_id,started,caller,route,data) VALUES(?,?,?,?,?)", row.id, now.UnixMilli(), "client", "route", string(data)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.moneyTimeline(context.Background(), now, "today", "total", nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *moneytimeline.Series
	for i := range got.Buckets {
		if len(got.Buckets[i].Series) > 0 {
			found = &got.Buckets[i].Series[0]
			break
		}
	}
	want := moneytimeline.TokenParts{Input: 8, Output: 4, CacheRead: 2}
	if found == nil || found.Tokens == nil || *found.Tokens != 14 || found.UnknownTokens != 2 || found.TokenParts == nil || *found.TokenParts != want {
		t.Fatalf("known token evidence lost: %+v", found)
	}
}
func TestMoneyTimelineKeepsAllClientAndProviderGroups(t *testing.T) {
	s := testService(t)
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	var clients, providers []moneytimeline.Series
	for i := range 8 {
		client, provider := fmt.Sprintf("client-%d", i), fmt.Sprintf("provider-%d", i)
		label := fmt.Sprintf("Client %d", i)
		if i == 7 {
			client, label = "total", "Total Labs"
		}
		insertTimelineEvidence(t, s, client, now, MoneyRecord{DeviceID: client, Device: label, Provider: provider, Money: Money{Billing: "pay_as_you_go", Kind: "unmeasured"}})
		clients = append(clients, moneytimeline.Series{Key: client, Label: label, Costs: []moneytimeline.Cost{}, UnknownCost: 1, UnknownTokens: 1})
		providers = append(providers, moneytimeline.Series{Key: provider, Label: provider, Costs: []moneytimeline.Cost{}, UnknownCost: 1, UnknownTokens: 1})
	}
	for _, tc := range []struct {
		group string
		want  []moneytimeline.Series
	}{
		{"client", clients},
		{"provider", providers},
		{"total", []moneytimeline.Series{{Key: "total", Label: "Total", Costs: []moneytimeline.Cost{}, UnknownCost: 8, UnknownTokens: 8}}},
	} {
		t.Run(tc.group, func(t *testing.T) {
			got, err := s.moneyTimeline(context.Background(), now, "7d", tc.group, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if series := got.Buckets[6].Series; !reflect.DeepEqual(series, tc.want) {
				t.Fatalf("series = %+v; want %+v", series, tc.want)
			}
		})
	}
}
func TestTimelineChargeOverflowRemainsUnknownWithoutSaturation(t *testing.T) {
	a := newTimelineAggregate("Total")
	currencies := map[string]bool{}
	for _, amount := range []int64{maxMoneyMicros - 2, 10} {
		accumulateTimelineRecord(a, timelineCharge("USD", amount), currencies)
	}
	series := timelineSeriesFromAggregate("total", a, sortedTimelineCurrencies(currencies))
	want := []moneytimeline.Cost{{Currency: "USD", Micros: maxMoneyMicros - 2, Estimated: true}}
	if !reflect.DeepEqual(series.Costs, want) || series.UnknownCost != 1 {
		t.Fatalf("overflow invented exact total: %+v", series)
	}
}
