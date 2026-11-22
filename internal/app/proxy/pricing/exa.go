package pricing

import "time"

// EstimatedExaSearch freezes the estimate reported in Exa's response. It is
// not a charge or a rate-card calculation.
func EstimatedExaSearch(amount *int64, ended int64) *Money {
	if amount == nil || *amount < 0 || *amount > MaxMoneyMicros {
		return nil
	}
	n := *amount
	return &Money{
		Billing: "pay_as_you_go", Kind: "estimated", Amount: &n, Currency: "USD",
		Source: "Exa response estimate (costDollars.total)", AsOf: time.UnixMilli(ended).UTC().Format(time.DateOnly),
	}
}
