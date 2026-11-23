package proxy

import (
	"net/url"
	"sort"

	"github.com/term-llm/term-llm/internal/app/proxy/pricing"
)

func finishPlanAPIEquivalent(in MoneyInput, route Route, observed *ObservedUsage, rates []MoneyRate, catalog catalogMoney) *APIEquivalent {
	catalog = withGrokBuildFallback(route, in.Model, catalog)
	if equivalent := pricing.PlanAPIEquivalent(in, rates); equivalent.Kind != "unpriced" || in.Model == "" || (in.Tier == "" && catalog == nil) {
		return equivalent
	}
	provider := offlinePriceProvider(route.Upstream)
	chatgpt := isChatGPTAPIEquivalentEndpoint(route)
	if chatgpt {
		provider = "openai"
	}
	m := finishTokenMoney(pricing.APIEquivalentInput(in), observed, rates, provider, catalog)
	equivalent := pricing.APIEquivalentFromMoney(m, in.Model)
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
