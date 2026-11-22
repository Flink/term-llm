package usagetap

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

const ExaSearch Protocol = "exa_search"

// ExaSearchJSON observes one bounded JSON response. Only use it for the
// trusted first-party Exa search endpoint. It does not collect token evidence.
type ExaSearchJSON struct {
	buf            []byte
	over, finished bool
	usage          Usage
}

func (p *ExaSearchJSON) Feed(data []byte) {
	if p.over || p.finished {
		return
	}
	if len(data) > MaxEventBytes-len(p.buf) {
		p.over, p.buf = true, nil
		return
	}
	p.buf = append(p.buf, data...)
}

func (p *ExaSearchJSON) Finish(complete bool) Usage {
	if !p.finished {
		p.finished = true
		p.usage = emptyUsage()
		if complete && !p.over {
			p.usage.ExaCostMicros = exaSearchCost(p.buf)
		}
		p.buf = nil
	}
	return p.usage
}

func exaSearchCost(data []byte) *int64 {
	var response struct {
		CostDollars struct {
			Total jsontext.Value `json:"total"`
		} `json:"costDollars"`
	}
	// v2 rejects duplicate names and matches the fields case-sensitively.
	if jsonv2.Unmarshal(data, &response) != nil {
		return nil
	}
	amount := parseCostDecimal(response.CostDollars.Total)
	if amount == nil {
		return nil
	}
	micros, ok := roundCostMicros(amount)
	if !ok {
		return nil
	}
	return &micros
}
