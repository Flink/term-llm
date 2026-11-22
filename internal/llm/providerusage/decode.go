package providerusage

import (
	"encoding/json"
	"errors"
)

// DecodeChatGPT decodes the account usage response without loading credentials
// or making any requests.
func DecodeChatGPT(data []byte) (*Report, error) {
	var raw chatGPTUsageResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return normalizeChatGPTProviderUsage(raw), nil
}

// DecodeGrok decodes the CLI billing response without making any requests.
func DecodeGrok(data []byte) (*Report, error) {
	var raw grokUsageResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if c := raw.Config; c != nil && c.CreditUsagePercent == nil && c.CurrentPeriod == nil && c.BillingPeriodStart == "" && c.BillingPeriodEnd == "" && (c.Used == nil || c.MonthlyLimit == nil) {
		return nil, errors.New("Grok billing did not report a usage period or allowance")
	}
	return normalizeGrokProviderUsage(raw)
}

// DecodeOpenCodeGo decodes the Go subscription usage response without making
// any requests.
func DecodeOpenCodeGo(data []byte) (*Report, error) {
	var raw openCodeGoUsageResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return normalizeOpenCodeGoProviderUsage(raw)
}
