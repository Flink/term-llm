package proxy

import "github.com/term-llm/term-llm/internal/app/proxy/pricing"

// Keep the proxy pricing API available to callers while pricing owns the types.
type (
	Money         = pricing.Money
	MoneyInput    = pricing.MoneyInput
	MoneyCharge   = pricing.MoneyCharge
	MoneyRate     = pricing.MoneyRate
	APIEquivalent = pricing.APIEquivalent
)

const maxMoneyMicros = pricing.MaxMoneyMicros

func validMoneyMetadata(currency, source, asof string) bool {
	return pricing.ValidMoneyMetadata(currency, source, asof)
}

// ValidateMoneyRates rejects ambiguous and undated operator price cards.
func ValidateMoneyRates(rates []MoneyRate) error { return pricing.ValidateMoneyRates(rates) }

// CalculateMoney prefers provider charges over estimates. Plans retain their
// actual billing state, with a separate counterfactual operator-card estimate.
func CalculateMoney(in MoneyInput, rates []MoneyRate) Money {
	return pricing.CalculateMoney(in, rates)
}
