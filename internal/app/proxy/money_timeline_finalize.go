package proxy

import (
	"maps"
	"slices"

	"github.com/term-llm/term-llm/internal/app/proxy/moneytimeline"
)

func timelineBucketSeries(aggregates map[string]*timelineAggregate, currencies []string) []moneytimeline.Series {
	series := make([]moneytimeline.Series, 0, len(aggregates))
	for _, key := range slices.Sorted(maps.Keys(aggregates)) {
		series = append(series, timelineSeriesFromAggregate(key, aggregates[key], currencies))
	}
	return series
}

func timelineSeriesFromAggregate(key string, a *timelineAggregate, currencies []string) moneytimeline.Series {
	series := moneytimeline.Series{Key: key, Label: a.label, Costs: []moneytimeline.Cost{}, UnknownCost: a.unknownCost, UnknownTokens: a.unknownTokens, Plan: a.plan}
	if a.tokenKnown {
		series.Tokens, series.TokenParts = new(a.tokens), new(a.tokenParts)
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
	currencies := slices.AppendSeq(make([]string, 0, len(seen)), maps.Keys(seen))
	slices.Sort(currencies)
	return currencies
}
