package proxy

import (
	"net/url"
	"sort"
)

// APIEquivalent is a frozen, counterfactual API estimate for plan activity, not
// a bill, spending, subscription savings, or evidence of identical products.
// Its kinds are estimated, estimated_floor, unpriced and unmeasured. Amount is
// absent when the usage or tariff cannot be established; unknown is never zero.
// It deliberately cannot contain another APIEquivalent or an actual charge.
type APIEquivalent struct {
	Kind     string     `json:"kind"`
	Model    string     `json:"model,omitempty"` // exact normalized response identity, never a request guess
	Amount   *int64     `json:"amount_micros,omitempty"`
	Currency string     `json:"currency,omitempty"`
	Source   string     `json:"source,omitempty"`
	AsOf     string     `json:"asof,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Rate     *MoneyRate `json:"rate,omitempty"`
}

const apiEquivalentReason = "Counterfactual API estimate only; not a bill, spending, or subscription savings."

func apiEquivalentInput(in MoneyInput) MoneyInput {
	in.Billing = "pay_as_you_go"
	in.Charge = nil // A provider charge is never a hypothetical API price.
	return in
}

func planAPIEquivalent(in MoneyInput, rates []MoneyRate) *APIEquivalent {
	if (in.UsageState == "reported" || in.UsageState == "partial") && (in.Model == "" || in.Tier == "") {
		return apiEquivalentFromMoney(Money{Kind: "unpriced", Reason: "Exact response model and service tier are not known."}, in.Model)
	}
	return apiEquivalentFromMoney(CalculateMoney(apiEquivalentInput(in), rates), in.Model)
}

func apiEquivalentFromMoney(m Money, model string) *APIEquivalent {
	reason := apiEquivalentReason
	if m.Reason != "" {
		reason += " " + m.Reason
	}
	return &APIEquivalent{Kind: m.Kind, Model: model, Amount: m.Amount, Currency: m.Currency, Source: m.Source, AsOf: m.AsOf, Reason: reason, Rate: m.Rate}
}

func finishPlanAPIEquivalent(in MoneyInput, route Route, observed *ObservedUsage, rates []MoneyRate, catalog catalogMoney) *APIEquivalent {
	if equivalent := planAPIEquivalent(in, rates); equivalent.Kind != "unpriced" || in.Model == "" || (in.Tier == "" && catalog == nil) {
		return equivalent
	}
	provider := offlinePriceProvider(route.Upstream)
	chatgpt := isChatGPTAPIEquivalentEndpoint(route)
	if chatgpt {
		provider = "openai"
	}
	m := finishTokenMoney(apiEquivalentInput(in), observed, rates, provider, catalog)
	equivalent := apiEquivalentFromMoney(m, in.Model)
	if chatgpt {
		equivalent.Reason += " ChatGPT usage mapped to public OpenAI API rates for the exact response model and tier; this does not assert the same product or charge."
	}
	return equivalent
}

// Account-backed ChatGPT has an explicit public API comparison, not an API
// tariff of its own. Accept only canonical backend roots used by the adapter;
// a route name, custom origin, model prefix or arbitrary path is not evidence.
func isChatGPTAPIEquivalentEndpoint(route Route) bool {
	if route.Account == "" {
		return false
	}
	u, err := url.Parse(route.Upstream)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if u.Host != "chatgpt.com" && u.Host != "chatgpt.com:443" {
		return false
	}
	switch u.Path {
	case "/backend-api/codex", "/backend-api/codex/":
		return true
	default:
		return false
	}
}

// Only plan rows contribute here. Legacy snapshots without an equivalent stay
// unpriced; reading history never fills them from today's rates. Invalid or
// overflowing estimates reduce coverage without changing actual spending.
func addAPIEquivalent(p *MoneyPeriod, equivalent *APIEquivalent) {
	if equivalent == nil || (equivalent.Kind != "estimated" && equivalent.Kind != "estimated_floor") {
		p.APIEquivalentUnpriced++
		return
	}
	m := Money{Kind: equivalent.Kind, Amount: equivalent.Amount, Currency: equivalent.Currency, Source: equivalent.Source, AsOf: equivalent.AsOf}
	if (m.Kind == "estimated_floor" && (m.Amount == nil || *m.Amount <= 0)) || !addMoneyTotal(&p.APIEquivalentTotals, m) {
		p.APIEquivalentUnpriced++
		return
	}
	p.APIEquivalentCovered++
}

func finalizeAPIEquivalentTotals(p *MoneyPeriod) {
	sort.Slice(p.APIEquivalentTotals, func(i, j int) bool { return p.APIEquivalentTotals[i].Currency < p.APIEquivalentTotals[j].Currency })
	for i := range p.APIEquivalentTotals {
		t := &p.APIEquivalentTotals[i]
		t.Qualifier = "about"
		if t.Floor > 0 || p.APIEquivalentUnpriced > 0 {
			t.Qualifier = "at_least"
		}
	}
}
