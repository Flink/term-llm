package proxy

import (
	"net/url"

	"github.com/term-llm/term-llm/internal/app/proxy/pricing"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
	"github.com/term-llm/term-llm/internal/usage"
)

// finishRequestMoney keeps explicit owner cards authoritative, including a card
// that cannot price a cache component. Plan comparisons are kept outside actual
// billing; bundled fallback never fills a card's holes or overrides a charge.
func finishRequestMoney(in MoneyInput, route Route, observed *ObservedUsage, rates []MoneyRate) Money {
	return finishRequestMoneyUsingCatalog(in, route, observed, rates, nil)
}

func finishRequestMoneyUsingCatalog(in MoneyInput, route Route, observed *ObservedUsage, rates []MoneyRate, catalog catalogMoney) Money {
	if in.Billing == "plan" && !in.RefusedBeforeDispatch {
		money := CalculateMoney(in, rates)
		money.APIEquivalent = finishPlanAPIEquivalent(in, route, observed, rates, catalog)
		return money
	}
	return finishTokenMoney(in, observed, rates, offlinePriceProvider(route.Upstream), catalog)
}

func finishTokenMoney(in MoneyInput, observed *ObservedUsage, rates []MoneyRate, provider string, catalog catalogMoney) Money {
	money := CalculateMoney(in, rates)
	if money.Kind != "unpriced" || ValidateMoneyRates(rates) != nil {
		return money
	}
	for _, r := range rates {
		if r.Provider == in.Provider && r.Model == in.Model && r.Tier == in.Tier {
			return money
		}
	}
	if catalog != nil {
		if priced, found := catalog(in, observed); found {
			return priced
		}
	}
	if observed == nil || observed.Usage.Ambiguous || in.UsageState != "reported" || observed.Usage.Source != usagetap.Reported || observed.Usage.Turns != 1 {
		money.Reason = "Bundled prices require complete single-response usage; request boundaries or the price tier are not known."
		return money
	}
	if !offlineUsageMatches(provider, in, *observed) {
		return money
	}
	price, ok := usage.LookupOfflinePricing(provider, in.Model)
	if !ok {
		return money
	}
	if provider == "openai" && price.Base.CacheWrite > 0 && observed.Usage.CacheWrite < 0 {
		money.Reason = "Separate cache-write usage is not reported for this nonzero write tariff; no zero write count is assumed."
		return money
	}
	if price.CacheWriteTTL != "" && in.CacheWriteTokens != 0 {
		money.Reason = "Cache-write TTL is not reported; no matching dated write price can be selected."
		return money
	}
	selected := price.Base
	if price.LongContext != nil {
		total, ok := pricing.TotalInput(in)
		if !ok {
			return money
		}
		if total > price.LongContextThreshold {
			selected = *price.LongContext
			price.Source += " (whole-request long-context rates)"
		}
	}
	card := MoneyRate{Provider: in.Provider, Model: in.Model, Tier: in.Tier, Currency: price.Currency, Source: price.Source, AsOf: price.AsOf, Input: &selected.Input, Output: &selected.Output, CacheRead: &selected.CacheRead}
	// The Anthropic bundled write price requires a 5m TTL that the observer
	// cannot attest. Omit it, even in a zero-write request's frozen price card.
	if price.CacheWriteTTL == "" {
		card.CacheWrite = &selected.CacheWrite
	}
	return CalculateMoney(in, []MoneyRate{card})
}

// Trust only the literal standard first-party base authority and path. Neither
// route names, presets, credential kinds nor model prefixes establish identity.
// Explicit :443 is the same HTTPS authority, never a fixture/custom-port bypass.
func offlinePriceProvider(upstream string) string {
	if usagetap.IsTypeSafeRoot(upstream) {
		return "typesafe"
	}
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return ""
	}
	switch u.Host {
	case "api.openai.com", "api.openai.com:443":
		if u.Path == "/v1" || u.Path == "/v1/" {
			return "openai"
		}
	case "api.anthropic.com", "api.anthropic.com:443":
		if u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/" {
			return "anthropic"
		}
	}
	return ""
}
func offlineUsageMatches(provider string, in MoneyInput, observed ObservedUsage) bool {
	u := observed.Usage
	if u.Model != in.Model || u.Tier != in.Tier || u.Input < 0 || u.Output < 0 || in.InputTokens < 0 || in.OutputTokens < 0 || in.CacheReadTokens < 0 || in.CacheWriteTokens < 0 {
		return false
	}
	switch provider {
	case "typesafe":
		return observed.Protocol == usagetap.TypeSafeSystemOne && in.Tier == "" && in.InputTokens == u.Input && in.OutputTokens == u.Output && in.CacheReadTokens == 0 && in.CacheWriteTokens == 0
	case "openai":
		// Responses/Chat input includes cache reads. Unknown reads cannot be
		// replaced with zero. Separate write counts are not understood here.
		return (observed.Protocol == usagetap.Responses || observed.Protocol == usagetap.ChatCompletions) && in.Tier == "default" && u.CachedInput >= 0 && u.CachedInput <= u.Input && (u.CacheWrite == -1 || u.CacheWrite == 0) && in.InputTokens == u.Input-u.CachedInput && in.CacheReadTokens == u.CachedInput && in.OutputTokens == u.Output && in.CacheWriteTokens == 0
	case "anthropic":
		// Messages attests service tier inside usage. Empty evidence is not an
		// attestation of the standard tariff; do not infer it.
		return observed.Protocol == usagetap.Messages && in.Tier == "standard" && u.CachedInput >= 0 && u.CacheWrite >= 0 && in.InputTokens == u.Input && in.OutputTokens == u.Output && in.CacheReadTokens == u.CachedInput && in.CacheWriteTokens == u.CacheWrite
	default:
		return false
	}
}
