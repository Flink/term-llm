package proxy

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/pricing"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
	"github.com/term-llm/term-llm/internal/modelsdev"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// catalogMoney prices an exact catalog entry without doing network or disk I/O.
// The boolean distinguishes an absent entry from an entry with incomplete rates:
// published-but-incomplete rates must not be filled from an older bundled table.
type catalogMoney func(MoneyInput, *ObservedUsage) (Money, bool)

func (s *Service) catalogMoney(route Route) catalogMoney {
	if s.pricing == nil {
		return nil
	}
	return func(in MoneyInput, observed *ObservedUsage) (Money, bool) {
		provider := catalogProvider(route, s.pricing)
		if provider == "" || in.Model == "" {
			return Money{}, false
		}
		model, fetched, found, comparison := lookupCatalogModel(s.pricing, route, provider, in.Model)
		if !found {
			return Money{}, false
		}
		if !catalogUsageMatches(in, observed) || (comparison != "" && !isSingleGrokBuildResponse(observed)) {
			return Money{Billing: in.Billing, Kind: "unpriced", Reason: "Usage cannot be matched to catalog token rates."}, true
		}
		// xAI documents the public 4.7 long-context tier as starting at 200K,
		// inclusive. Keep this exception scoped to the 4.7 Build comparison.
		inclusiveThreshold := 0
		if comparison != "" && in.Model == "grok-4.7-build" {
			inclusiveThreshold = 200_000
		}
		money := pricing.CatalogMoney(in, provider, model, fetched, observed.Usage.Turns, inclusiveThreshold)
		if money.Amount != nil && comparison != "" {
			money.Reason = comparison
		}
		return money, true
	}
}

// Prefer the exact catalog entry, even if it has incomplete prices. On the
// native Grok plan only, standard Build IDs can use their matching public model.
func lookupCatalogModel(catalog *modelsdev.Client, route Route, provider, id string) (modelsdev.Model, time.Time, bool, string) {
	model, fetched, found := catalog.Lookup(provider, id)
	if !found && provider == "xai" {
		if base := grokBuildAPIModel(id); base != "" && isGrokBuildPlan(route) {
			model, fetched, found = catalog.Lookup(provider, base)
			return model, fetched, found, "Compared with public " + base + " API token rates."
		}
	}
	return model, fetched, found, ""
}

func catalogUsageMatches(in MoneyInput, observed *ObservedUsage) bool {
	if observed == nil || observed.Usage.Ambiguous || in.UsageState != "reported" || observed.Usage.Source != usagetap.Reported {
		return false
	}
	if observed.Protocol == usagetap.TypeSafeSystemOne {
		return offlineUsageMatches("typesafe", in, *observed)
	}
	u := observed.Usage
	if u.Model != in.Model || u.Input < 0 || u.Output < 0 || u.CachedInput < 0 || in.InputTokens < 0 || in.OutputTokens < 0 || in.CacheReadTokens < 0 || in.CacheWriteTokens < 0 {
		return false
	}
	switch observed.Protocol {
	case usagetap.Messages, usagetap.Bedrock:
		return u.CacheWrite >= 0 && in.InputTokens == u.Input && in.OutputTokens == u.Output && in.CacheReadTokens == u.CachedInput && in.CacheWriteTokens == u.CacheWrite
	case usagetap.Responses, usagetap.ChatCompletions, usagetap.Gemini:
		output := u.Output
		if observed.Protocol == usagetap.Gemini {
			if u.Reasoning < 0 || u.Reasoning > math.MaxInt64-output {
				return false
			}
			output += u.Reasoning
		}
		return u.CachedInput <= u.Input && u.CacheWrite <= 0 && in.InputTokens == u.Input-u.CachedInput && in.OutputTokens == output && in.CacheReadTokens == u.CachedInput && in.CacheWriteTokens == 0
	default:
		return false
	}
}

// Reuse the canonical native-client routing table rather than maintaining a
// second endpoint registry. Catalog API metadata also supports other providers.
// Route names, arbitrary model prefixes and lookalike hosts establish nothing.
func catalogProvider(route Route, catalog *modelsdev.Client) string {
	if provider := offlinePriceProvider(route.Upstream); provider != "" {
		return provider
	}
	if isChatGPTAPIEquivalentEndpoint(route) {
		return "openai"
	}
	endpoint := priceEndpoint(route.Upstream)
	if endpoint == "" {
		return ""
	}
	for _, profile := range proxyprofile.All() {
		if profile.Upstream == "" || endpoint != priceEndpoint(profile.Upstream) || profile.Account != "" && route.Account == "" {
			continue
		}
		provider := string(profile.Provider)
		if profile.ID == "opencode-go-messages" || profile.ID == "opencode-go-gemini" {
			provider = "opencode-go"
		}
		switch provider {
		case "gemini", "agy-bin":
			provider = "google"
		case "zen":
			provider = "opencode"
		case "claude-bin":
			provider = "anthropic"
		case "grok", "grok-bin":
			provider = "xai"
		case "copilot":
			provider = "github-copilot"
		case "chatgpt":
			provider = "openai"
		}
		if provider != "" {
			return provider
		}
	}
	provider, _ := catalog.ProviderForAPI(route.Upstream)
	return provider
}

func priceEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimSuffix(host, ":443")
	return "https://" + host + strings.TrimSuffix(u.Path, "/")
}
