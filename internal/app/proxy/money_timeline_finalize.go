package proxy

import (
	"sort"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
)

func finalizeTimelineBuckets(result *moneytimeline.Response, aggs []map[string]*timelineAggregate) {
	for i := range result.Buckets {
		result.Buckets[i].Series = timelineBucketSeries(aggs[i], result.Currencies)
	}
}

func timelineBucketSeries(aggregates map[string]*timelineAggregate, currencies []string) []moneytimeline.Series {
	keys := make([]string, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	series := make([]moneytimeline.Series, 0, len(keys))
	for _, key := range keys {
		series = append(series, timelineSeriesFromAggregate(key, aggregates[key], currencies))
	}
	return series
}

func timelineSeriesFromAggregate(key string, a *timelineAggregate, currencies []string) moneytimeline.Series {
	series := moneytimeline.Series{Key: key, Label: a.label, Costs: []moneytimeline.Cost{}, UnknownCost: a.unknownCost, UnknownTokens: a.unknownTokens, Plan: a.plan}
	if a.tokenKnown {
		n, parts := a.tokens, a.tokenParts
		series.Tokens = &n
		series.TokenParts = &parts
	}
	for _, currency := range currencies {
		amount, hasCost := a.cost[currency]
		cost := moneytimeline.Cost{Currency: currency, Micros: amount, Estimated: a.estimated[currency]}
		plan, hasPlan := a.planCost[currency]
		if hasPlan {
			// Keep the original plotted amount if adding plan estimates would
			// exceed the range that JSON consumers can represent exactly.
			if plan.micros > maxMoneyMicros-cost.Micros {
				series.UnknownCost += plan.complete
			} else {
				cost.Micros += plan.micros
				cost.Estimated = true
				cost.PlanEstimateMicros = &plan.micros
			}
		}
		if hasCost || cost.PlanEstimateMicros != nil {
			series.Costs = append(series.Costs, cost)
		}
	}
	return series
}

func sortedTimelineCurrencies(seen map[string]bool) []string {
	currencies := make([]string, 0, len(seen))
	for c := range seen {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	return currencies
}
