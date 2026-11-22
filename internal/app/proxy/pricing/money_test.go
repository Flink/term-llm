package pricing

import "testing"

func moneyPtr(n int64) *int64 { return &n }
func testMoneyRate() MoneyRate {
	return MoneyRate{Provider: "fixture", Model: "exact", Currency: "USD", Source: "operator fixture", AsOf: "2026-11-01", Input: moneyPtr(2000000), Output: moneyPtr(6000000), CacheRead: moneyPtr(500000), CacheWrite: moneyPtr(3000000)}
}

func TestCalculateMoney(t *testing.T) {
	rate := testMoneyRate()
	base := MoneyInput{Billing: "pay_as_you_go", Provider: "fixture", Model: "exact", UsageState: "reported", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 100, CacheWriteTokens: 50}
	tests := []struct {
		name   string
		change func(*MoneyInput)
		kind   string
		amount int64
	}{
		{"estimate", func(*MoneyInput) {}, "estimated", 3400},
		{"partial", func(i *MoneyInput) { i.UsageState = "partial" }, "estimated_floor", 3400},
		{"provider preferred", func(i *MoneyInput) {
			i.Charge = &MoneyCharge{AmountMicros: 123, Currency: "EUR", Source: "vendor", AsOf: "2026-11-15"}
		}, "charged", 123},
		{"plan never priced", func(i *MoneyInput) {
			i.Billing = "plan"
			i.Charge = &MoneyCharge{AmountMicros: 999, Currency: "USD", Source: "vendor", AsOf: "2026-11-15"}
		}, "included", -1},
		{"local", func(i *MoneyInput) { i.Billing = "local" }, "none", -1},
		{"unspecified", func(i *MoneyInput) { i.Billing = "" }, "unspecified", -1},
		{"no usage", func(i *MoneyInput) { i.UsageState = "unknown" }, "unmeasured", -1},
		{"unknown model", func(i *MoneyInput) { i.Model = "exact-new" }, "unpriced", -1},
		{"other provider", func(i *MoneyInput) { i.Provider = "aggregator" }, "unpriced", -1},
		{"other tier", func(i *MoneyInput) { i.Tier = "priority" }, "unpriced", -1},
		{"negative usage", func(i *MoneyInput) { i.InputTokens = -1 }, "unpriced", -1},
		{"refused", func(i *MoneyInput) { i.RefusedBeforeDispatch = true }, "no_charge_expected", -1},
		{"bad charge", func(i *MoneyInput) { i.Charge = &MoneyCharge{AmountMicros: 1, Currency: "$"} }, "unmeasured", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.change(&in)
			got := CalculateMoney(in, []MoneyRate{rate})
			if got.Kind != tt.kind {
				t.Fatalf("%+v", got)
			}
			if tt.amount < 0 {
				if got.Amount != nil || got.Currency != "" {
					t.Fatalf("non-money amount: %+v", got)
				}
			} else if got.Amount == nil || *got.Amount != tt.amount {
				t.Fatalf("amount: %+v", got)
			}
		})
	}
	t.Run("cache rate absent", func(t *testing.T) {
		rate.CacheRead = nil
		if got := CalculateMoney(base, []MoneyRate{rate}); got.Kind != "unpriced" {
			t.Fatal(got)
		}
	})
}
func TestMoneyRateValidation(t *testing.T) {
	t.Run("missing is not free", func(t *testing.T) {
		r := testMoneyRate()
		r.Input = nil
		if err := ValidateMoneyRates([]MoneyRate{r}); err == nil {
			t.Fatal("missing input price accepted as zero")
		}
	})
	r := testMoneyRate()
	if err := ValidateMoneyRates([]MoneyRate{r, r}); err == nil {
		t.Fatal("duplicate accepted")
	}
	r.AsOf = "yesterday"
	if err := ValidateMoneyRates([]MoneyRate{r}); err == nil {
		t.Fatal("undated accepted")
	}
	r = testMoneyRate()
	r.Input = moneyPtr(-1)
	if err := ValidateMoneyRates([]MoneyRate{r}); err == nil {
		t.Fatal("negative accepted")
	}
}
func TestMoneyIntegerRoundingAndOverflow(t *testing.T) {
	r := testMoneyRate()
	r.Input = moneyPtr(1)
	in := MoneyInput{Billing: "pay_as_you_go", Provider: r.Provider, Model: r.Model, UsageState: "reported", InputTokens: 1}
	m := CalculateMoney(in, []MoneyRate{r})
	if m.Amount == nil || *m.Amount != 1 {
		t.Fatal(m)
	}
	in.InputTokens = 1 << 62
	r.Input = moneyPtr(MaxMoneyMicros)
	if got := CalculateMoney(in, []MoneyRate{r}); got.Kind != "unpriced" {
		t.Fatal(got)
	}
}

func TestMoneyPartialZeroIsUnknown(t *testing.T) {
	r := testMoneyRate()
	m := CalculateMoney(MoneyInput{Billing: "pay_as_you_go", Provider: r.Provider, Model: r.Model, UsageState: "partial"}, []MoneyRate{r})
	if m.Amount != nil || m.Kind != "unmeasured" {
		t.Fatalf("partial became a zero bill: %+v", m)
	}
}

func TestMoneyFreezesRateAndCacheSemantics(t *testing.T) {
	r := testMoneyRate()
	r.Tier = "priority-long-context"
	in := MoneyInput{Billing: "pay_as_you_go", Provider: r.Provider, Model: r.Model, Tier: r.Tier, UsageState: "reported", InputTokens: 100, CacheReadTokens: 100}
	rates := []MoneyRate{r}
	m := CalculateMoney(in, rates)
	*r.CacheRead = 9999999
	*rates[0].Input = 9999999
	if m.Rate == nil || m.Rate.Tier != in.Tier || *m.Rate.CacheRead != 500000 || *m.Rate.Input != 2000000 {
		t.Fatalf("rate not frozen: %+v", m.Rate)
	}
	if m.Amount == nil || *m.Amount != 250 {
		t.Fatalf("cache counts double counted: %+v", m)
	}
}
