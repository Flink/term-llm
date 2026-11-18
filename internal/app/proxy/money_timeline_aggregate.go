package proxy

import (
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func timelineBucketIndex(t time.Time, bounds []time.Time) int {
	for i := 0; i < len(bounds)-1; i++ {
		if !t.Before(bounds[i]) && (t.Before(bounds[i+1]) || (i == len(bounds)-2 && t.Equal(bounds[i+1]))) {
			return i
		}
	}
	return -1
}

func timelineGroupIdentity(rec MoneyRecord, caller, route, group string) (string, string) {
	if group == "client" {
		key := rec.DeviceID
		if key == "" {
			key = caller
		}
		label := rec.Device
		if label == "" {
			label = "Unknown client"
		}
		return key, label
	}
	if group == "provider" {
		key := rec.Provider
		if key == "" {
			key = route
		}
		label := timelineProviderLabel(key)
		if label == "" {
			label = key
		}
		if label == "" {
			label = "Unknown provider"
		}
		return key, label
	}
	if group == "model" {
		model := timelineModel(rec)
		if model == "" {
			return "model:", "Unknown model"
		}
		return "model:" + model, model
	}
	return "total", "Total"
}

func timelineModel(rec MoneyRecord) string {
	if rec.Usage != nil {
		if rec.Usage.Usage.Ambiguous {
			return ""
		}
		if model := rec.Usage.Usage.Model; model != "" {
			return model
		}
	}
	// A stored plan estimate can preserve the response identity when the
	// original usage snapshot is no longer available.
	if equivalent := rec.Money.APIEquivalent; equivalent != nil {
		return equivalent.Model
	}
	return ""
}

func supportedTimelineProtocol(p usagetap.Protocol) bool {
	switch p {
	case usagetap.Responses, usagetap.ChatCompletions, usagetap.Messages, usagetap.Bedrock, usagetap.Gemini, usagetap.Ollama:
		return true
	default:
		return false
	}
}

func accumulateTimelineRecord(a *timelineAggregate, rec MoneyRecord, currencies map[string]bool) {
	a.requests++
	accumulateTimelineCost(a, rec.Money, currencies)
	if rec.Usage == nil || rec.Usage.Usage.Source != "reported" || !supportedTimelineProtocol(rec.Usage.Protocol) {
		a.unknownTokens++
		return
	}
	mi := moneyInputFromUsage(MoneyInput{}, *rec.Usage)
	values := []int64{mi.InputTokens, mi.OutputTokens, mi.CacheReadTokens, mi.CacheWriteTokens}
	total := int64(0)
	for _, value := range values {
		if value < 0 || total > maxMoneyMicros-value {
			a.unknownTokens++
			return
		}
		total += value
	}
	if a.tokens > maxMoneyMicros-total {
		a.unknownTokens++
		return
	}
	a.tokens += total
	a.tokenKnown = true
}

func accumulateTimelineCost(a *timelineAggregate, m Money, currencies map[string]bool) {
	if m.Billing == "plan" {
		a.plan++
		// As in the summary, a request known not to have been dispatched
		// does not reduce estimate coverage.
		if m.Kind != "no_charge_expected" {
			accumulateTimelinePlanCost(a, m.APIEquivalent, currencies)
		}
		return
	}
	if m.Billing == "pay_as_you_go" && m.Amount != nil && *m.Amount >= 0 && *m.Amount <= maxMoneyMicros && validMoneyMetadata(m.Currency, m.Source, m.AsOf) && (m.Kind == "charged" || m.Kind == "estimated" || m.Kind == "estimated_floor") {
		if *m.Amount > maxMoneyMicros-a.cost[m.Currency] {
			a.unknownCost++
			a.estimated[m.Currency] = true
		} else {
			a.cost[m.Currency] += *m.Amount
			currencies[m.Currency] = true
			if m.Kind != "charged" {
				a.estimated[m.Currency] = true
			}
			if m.Kind == "estimated_floor" {
				a.unknownCost++
			}
		}
	} else if m.Billing != "local" && m.Kind != "no_charge_expected" {
		a.unknownCost++
	}
}

func accumulateTimelinePlanCost(a *timelineAggregate, e *APIEquivalent, currencies map[string]bool) {
	// Mirror addAPIEquivalent's frozen evidence checks: no repricing,
	// and a partial estimate must establish a positive lower bound.
	if e == nil || (e.Kind != "estimated" && e.Kind != "estimated_floor") || e.Amount == nil ||
		*e.Amount < 0 || *e.Amount > maxMoneyMicros || !validMoneyMetadata(e.Currency, e.Source, e.AsOf) ||
		(e.Kind == "estimated_floor" && *e.Amount == 0) {
		a.unknownCost++
		return
	}
	cost := timelinePlanCost{micros: *e.Amount}
	if e.Kind == "estimated_floor" {
		a.unknownCost++
	} else {
		cost.complete = 1
	}
	if a.addPlanCost(e.Currency, cost) {
		currencies[e.Currency] = true
	}
}

func (a *timelineAggregate) addPlanCost(currency string, cost timelinePlanCost) bool {
	current := a.planCost[currency]
	if cost.micros > maxMoneyMicros-current.micros {
		a.unknownCost += cost.complete
		return false
	}
	if a.planCost == nil {
		a.planCost = make(map[string]timelinePlanCost)
	}
	current.micros += cost.micros
	current.complete += cost.complete
	a.planCost[currency] = current
	return true
}
