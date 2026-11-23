package proxy

import (
	"regexp"

	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

var standardGrokBuildID = regexp.MustCompile(`^(grok-[0-9]+(?:\.[0-9]+)?)-build$`)

func grokBuildAPIModel(id string) string {
	if match := standardGrokBuildID.FindStringSubmatch(id); match != nil {
		return match[1]
	}
	return ""
}

func isGrokBuildPlan(route Route) bool {
	profile, ok := proxyprofile.ByID("grok")
	return ok && route.Billing == "plan" && route.Account != "" && priceEndpoint(route.Upstream) == priceEndpoint(profile.Upstream)
}

// withGrokBuildFallback supplies dated first-party rates only where the catalog
// has no entry. Never infer a future Fast rate from a standard API model.
func withGrokBuildFallback(route Route, model string, catalog catalogMoney) catalogMoney {
	if (model != "grok-4.7-build" && model != "grok-4.7-build-fast") || !isGrokBuildPlan(route) {
		return catalog
	}
	return func(in MoneyInput, observed *ObservedUsage) (Money, bool) {
		if catalog != nil {
			if money, found := catalog(in, observed); found {
				return money, true
			}
		}
		return grokBuildPlanMoney(in, observed), true
	}
}

func isSingleGrokBuildResponse(observed *ObservedUsage) bool {
	return observed != nil && observed.Protocol == usagetap.Responses && observed.Usage.Turns == 1
}

func grokBuildPlanMoney(in MoneyInput, observed *ObservedUsage) Money {
	if !isSingleGrokBuildResponse(observed) || !catalogUsageMatches(in, observed) {
		return Money{Kind: "unpriced", Reason: "Grok Build pricing requires complete usage from a single response."}
	}
	if in.Tier != "" && in.Tier != "default" && in.Tier != "standard" {
		return Money{Kind: "unpriced", Reason: "No published Grok Build rate for this service tier."}
	}

	// Source: https://docs.x.ai/developers/pricing (updated October 10, 2026).
	// The text API uses the long-context rate from 200k prompt tokens; the
	// separate Fast table specifies that it applies when the prompt exceeds 200k.
	const threshold = 200_000
	input, cached, output := int64(2_000_000), int64(500_000), int64(6_000_000)
	reason := "Compared with public Grok 4.7 API token rates."
	if in.Model == "grok-4.7-build-fast" {
		input, cached, output = 4_000_000, 1_000_000, 12_000_000
		if observed.Usage.Input > threshold {
			input, cached, output = 6_000_000, 1_500_000, 18_000_000
		}
		reason = "Uses published Grok 4.7 Fast token rates for Grok Build; Fast is not available on the public API."
	} else if observed.Usage.Input >= threshold {
		input, cached, output = 4_000_000, 1_000_000, 12_000_000
	}
	card := MoneyRate{
		Provider: in.Provider, Model: in.Model, Tier: in.Tier,
		Currency: "USD", Source: "https://docs.x.ai/developers/pricing", AsOf: "2026-10-10",
		Input: &input, CacheRead: &cached, Output: &output,
	}
	money := CalculateMoney(in, []MoneyRate{card})
	if money.Amount != nil {
		money.Reason = reason
	}
	return money
}
