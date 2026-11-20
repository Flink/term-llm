package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
)

func validTimelineGroup(group string) bool {
	switch group {
	case "total", "client", "provider", "model":
		return true
	default:
		return false
	}
}

// An explicit location affects this chart only. Callers without one keep the
// configured money timezone, which still governs summaries and budgets.
func (s *Service) moneyTimeline(ctx context.Context, now time.Time, period, group string, loc *time.Location) (moneytimeline.Response, error) {
	if !validTimelineGroup(group) {
		return moneytimeline.Response{}, fmt.Errorf("invalid group")
	}
	if loc == nil {
		settings, err := s.currentMoneySettings(ctx)
		if err != nil {
			return moneytimeline.Response{}, err
		}
		loc, err = time.LoadLocation(settings.Timezone) // An empty timezone means UTC.
		if err != nil {
			return moneytimeline.Response{}, err
		}
	}
	result, err := moneytimeline.New(now, loc, period)
	if err != nil {
		return moneytimeline.Response{}, err
	}
	from, through := result.Start.UnixMilli(), result.End.UnixMilli()
	// Repair retained rows before reading so pre-feature requests are represented
	// and missing response observations can be backfilled without repricing.
	if err := s.store.reconcileMoneyHistory(ctx, from, through); err != nil {
		return moneytimeline.Response{}, fmt.Errorf("reconcile current timeline: %w", err)
	}
	aggs := make([]map[string]*timelineAggregate, len(result.Buckets))
	currencySet := map[string]bool{}
	err = s.store.streamTimelineRows(ctx, from, through, func(ms int64, caller, route string, rec MoneyRecord) {
		idx := result.BucketIndex(time.UnixMilli(ms))
		if idx < 0 {
			return
		}
		if aggs[idx] == nil {
			aggs[idx] = map[string]*timelineAggregate{}
		}
		key, label := timelineGroupIdentity(rec, caller, route, group)
		a := aggs[idx][key]
		if a == nil {
			a = newTimelineAggregate(label)
			aggs[idx][key] = a
		}
		accumulateTimelineRecord(a, rec, currencySet)
	})
	if err != nil {
		return moneytimeline.Response{}, fmt.Errorf("read current timeline: %w", err)
	}
	result.Currencies = sortedTimelineCurrencies(currencySet)
	finalizeTimelineBuckets(&result, aggs)
	if result.Previous, err = s.timelinePrevious(ctx, result); err != nil {
		return moneytimeline.Response{}, err
	}
	return result, nil
}

// streamTimelineRows reads all retained financial rows in an inclusive
// millisecond interval. It does not load technical requests or their bodies.
func (s *store) streamTimelineRows(ctx context.Context, from, through int64, visit func(ms int64, caller, route string, rec MoneyRecord)) error {
	rows, err := s.db.QueryContext(ctx, "SELECT started,caller,route,data FROM money_history WHERE started>=? AND started<=? ORDER BY started,request_id", from, through)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ms int64
		var caller, route, data string
		if err := rows.Scan(&ms, &caller, &route, &data); err != nil {
			return err
		}
		var rec MoneyRecord
		if json.Unmarshal([]byte(data), &rec) != nil {
			rec.Money = *pendingRequestMoney("unspecified")
		}
		visit(ms, caller, route, rec)
	}
	return rows.Err()
}

func (s *Service) timelineAPI(w http.ResponseWriter, r *http.Request) bool {
	period := r.URL.Query().Get("range")
	if period == "" {
		period = "30d"
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "total"
	}
	if !moneytimeline.ValidRange(period) || !validTimelineGroup(group) {
		fail(w, 400, "Invalid timeline range or grouping")
		return true
	}
	var loc *time.Location
	if zone := r.URL.Query().Get("timezone"); zone != "" {
		var err error
		loc, err = time.LoadLocation(zone)
		if err != nil || zone == "Local" {
			fail(w, 400, "Invalid timeline timezone")
			return true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.moneyTimeline(ctx, time.Now(), period, group, loc)
	if err != nil {
		fail(w, 503, "Timeline unavailable; no missing usage has been assumed to be zero")
		return true
	}
	jsonReply(w, 200, out)
	return true
}
