package pricing

import (
	"math"
	"time"

	"github.com/term-llm/term-llm/internal/modelsdev"
)

// CatalogMoney prices an already selected catalog entry against normalized,
// validated usage. The caller is responsible for route and usage identity.
// A nonzero inclusiveThreshold opts one documented tier into an inclusive cutoff.
func CatalogMoney(in MoneyInput, provider string, model modelsdev.Model, fetched time.Time, turns int64, inclusiveThreshold int) Money {
	unavailable := func(reason string) Money {
		return Money{Billing: in.Billing, Kind: "unpriced", Reason: reason}
	}
	if model.Cost == nil || fetched.IsZero() {
		return unavailable("models.dev has no price for this model.")
	}
	if in.Tier != "" && in.Tier != "default" && in.Tier != "standard" {
		return unavailable("models.dev does not price this service tier.")
	}
	cost := *model.Cost
	total, ok := TotalInput(in)
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
		if turns != 1 {
			return unavailable("Context-tier pricing needs single-response usage.")
		}
		size := tier.Tier.Size
		if size > selectedThreshold && (total > int64(size) || (size == inclusiveThreshold && total == int64(size))) {
			selectedThreshold = size
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
	return money
}

func catalogMicros(rate *float64) (*int64, bool) {
	if rate == nil {
		return nil, true
	}
	scaled := *rate * 1_000_000
	rounded := math.Round(scaled)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || rounded > float64(MaxMoneyMicros) || math.Abs(scaled-rounded) > 0.000001 {
		return nil, false
	}
	value := int64(rounded)
	return &value, true
}

// TotalInput includes cached reads and writes for context-tier selection.
func TotalInput(in MoneyInput) (int64, bool) {
	total := int64(0)
	for _, count := range []int64{in.InputTokens, in.CacheReadTokens, in.CacheWriteTokens} {
		if count < 0 || count > math.MaxInt64-total {
			return 0, false
		}
		total += count
	}
	return total, true
}
