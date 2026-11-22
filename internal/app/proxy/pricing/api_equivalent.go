package pricing

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

// APIEquivalentInput prepares a token-pricing input for a plan comparison.
func APIEquivalentInput(in MoneyInput) MoneyInput {
	in.Billing = "pay_as_you_go"
	in.Charge = nil // A provider charge is never a hypothetical API price.
	return in
}

// PlanAPIEquivalent prices plan activity against the exact operator card.
func PlanAPIEquivalent(in MoneyInput, rates []MoneyRate) *APIEquivalent {
	if (in.UsageState == "reported" || in.UsageState == "partial") && (in.Model == "" || in.Tier == "") {
		return APIEquivalentFromMoney(Money{Kind: "unpriced", Reason: "Exact response model and service tier are not known."}, in.Model)
	}
	return APIEquivalentFromMoney(CalculateMoney(APIEquivalentInput(in), rates), in.Model)
}

// APIEquivalentFromMoney labels a frozen token price as a counterfactual estimate.
func APIEquivalentFromMoney(m Money, model string) *APIEquivalent {
	reason := apiEquivalentReason
	if m.Reason != "" {
		reason += " " + m.Reason
	}
	return &APIEquivalent{Kind: m.Kind, Model: model, Amount: m.Amount, Currency: m.Currency, Source: m.Source, AsOf: m.AsOf, Reason: reason, Rate: m.Rate}
}
