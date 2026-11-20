package proxy

import (
	"math"
	"net/url"
	"strings"

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
		model, fetched, found := s.pricing.Lookup(provider, in.Model)
		if !found {
			return Money{}, false
		}
		unavailable := func(reason string) (Money, bool) {
			return Money{Billing: in.Billing, Kind: "unpriced", Reason: reason}, true
		}
		if model.Cost == nil || fetched.IsZero() {
			return unavailable("models.dev has no price for this model.")
		}
		if !catalogUsageMatches(in, observed) {
			return unavailable("Usage cannot be matched to catalog token rates.")
		}
		if in.Tier != "" && in.Tier != "default" && in.Tier != "standard" {
			return unavailable("models.dev does not price this service tier.")
		}
		cost := *model.Cost
		total, ok := offlineTotalInput(in)
		if !ok {
			return unavailable("Input usage is incomplete.")
		}
		selectedThreshold := 0
		for _, tier := range cost.Tiers {
			if tier.Tier.Type != "context" {
				continue
			}
			if tier.Tier.Size <= 0 {
				return unavailable("Invalid catalog context tier.")
			}
			if observed.Usage.Turns != 1 {
				return unavailable("Context-tier pricing needs single-response usage.")
			}
			if total > int64(tier.Tier.Size) && tier.Tier.Size > selectedThreshold {
				selectedThreshold = tier.Tier.Size
				cost.Input, cost.Output = tier.Input, tier.Output
				cost.CacheRead, cost.CacheWrite = tier.CacheRead, tier.CacheWrite
			}
		}
		card := MoneyRate{Provider: in.Provider, Model: in.Model, Tier: in.Tier, Currency: "USD", Source: "models.dev/" + provider, AsOf: fetched.UTC().Format("2006-01-02")}
		rates := []*float64{cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite}
		destinations := []**int64{&card.Input, &card.Output, &card.CacheRead, &card.CacheWrite}
		for i, rate := range rates {
			converted, valid := catalogMicros(rate)
			if !valid {
				return unavailable("Catalog price is out of range.")
			}
			*destinations[i] = converted
		}
		if card.Input == nil || card.Output == nil {
			return unavailable("models.dev has incomplete input/output rates.")
		}
		money := CalculateMoney(in, []MoneyRate{card})
		if money.Amount != nil && in.CacheWriteTokens > 0 {
			money.Reason = "Cache writes use the catalog default rate; TTL-specific rates may differ."
		}
		return money, true
	}
}

func catalogMicros(rate *float64) (*int64, bool) {
	if rate == nil {
		return nil, true
	}
	scaled := *rate * 1_000_000
	rounded := math.Round(scaled)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || rounded > float64(maxMoneyMicros) || math.Abs(scaled-rounded) > 0.000001 {
		return nil, false
	}
	value := int64(rounded)
	return &value, true
}

func catalogUsageMatches(in MoneyInput, observed *ObservedUsage) bool {
	if observed == nil || observed.Usage.Ambiguous || in.UsageState != "reported" || observed.Usage.Source != usagetap.Reported {
		return false
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
