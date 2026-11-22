package proxy

import (
	"github.com/term-llm/term-llm/internal/app/proxy/pricing"
	"github.com/term-llm/term-llm/internal/app/proxy/usagetap"
)

func estimatedExaMoney(rec *Record, route Route) *Money {
	if route.Billing != "pay_as_you_go" || rec.Outcome != "complete" || rec.Status < 200 || rec.Status >= 300 || rec.Ended <= 0 || rec.Usage == nil || rec.Usage.Protocol != usagetap.ExaSearch {
		return nil
	}
	return pricing.EstimatedExaSearch(rec.Usage.Usage.ExaCostMicros, rec.Ended)
}
