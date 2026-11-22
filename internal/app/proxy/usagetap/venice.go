package usagetap

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
)

const (
	maxVeniceCostBytes = 512
	maxCostNumberBytes = 64
	maxCostExponent    = 64
)

// veniceCharge is independent of token-count evidence. It holds only the last
// event's candidate charge, never the raw event or a sum of cumulative chunks.
type veniceCharge struct {
	micros                int64
	present, bad, sawDone bool
}

// ForVeniceChatCompletions enables provider cost parsing for an HTTP response.
// The caller must first authenticate the route, response origin and endpoint.
// Only JSON and SSE are supported for the charge (including detected framing).
func ForVeniceChatCompletions(contentType string) *Parser {
	p := ForResponse("/chat/completions", contentType)
	if p.format == JSON || p.format == SSE || p.format == missingContentType {
		p.venice = &veniceCharge{}
	}
	return p
}

func (v *veniceCharge) observe(data []byte, o object) {
	v.present = false
	if v.bad || v.sawDone {
		return
	}
	var cost json.RawMessage
	if usage := o["usage"]; len(usage) > 0 && usage[0] == '{' {
		var ok bool
		cost, ok = uniqueJSONField(usage, "cost")
		if !ok {
			return
		}
	}
	// Venice also reports cost as a sibling of usage. Prefer usage.cost if
	// present so the two representations are never added together.
	if cost == nil && o["cost"] != nil {
		cost, _ = uniqueJSONField(data, "cost")
	}
	total := veniceCostTotal(cost)
	if total == nil {
		return
	}
	if _, ok := uniqueJSONField(data, "usage"); !ok {
		return
	}
	v.micros, v.present = roundCostMicros(total)
}

// uniqueJSONField avoids the last-key-wins behavior of a map for charge fields.
// It returns nil, true for an absent field and nil, false for ambiguity or an
// invalid object. The caller has already checked that the event is valid JSON.
func uniqueJSONField(data []byte, field string) (json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return nil, false
	}
	var found, value json.RawMessage
	for d.More() {
		key, err := d.Token()
		if err != nil || d.Decode(&value) != nil {
			return nil, false
		}
		if key == field {
			if found != nil {
				return nil, false
			}
			found, value = value, nil
		}
	}
	return found, true
}

func veniceCostTotal(data []byte) *big.Rat {
	if len(data) == 0 || len(data) > maxVeniceCostBytes {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return nil
	}
	var total big.Rat
	seen := make(map[string]bool, 2)
	for d.More() {
		token, err := d.Token()
		key, _ := token.(string)
		if err != nil {
			return nil
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil
		}
		if key == "diam" {
			continue // DIAM is not included in the USD charge.
		}
		if (key != "diem" && key != "usd") || seen[key] {
			return nil
		}
		seen[key] = true
		amount := parseCostDecimal(value)
		if amount == nil {
			return nil
		}
		total.Add(&total, amount) // 1 DIEM = 1 USD.
	}
	if len(seen) == 0 {
		return nil
	}
	return &total
}

func parseCostDecimal(data []byte) *big.Rat {
	// These bytes are a complete JSON value. Only a digit can start an
	// unsigned JSON number; reject strings, null and even signed zero.
	if len(data) == 0 || len(data) > maxCostNumberBytes || data[0] < '0' || data[0] > '9' {
		return nil
	}
	// Bound the exponent independently of length before constructing a rational.
	if i := bytes.IndexAny(data, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(string(data[i+1:]))
		if err != nil || exponent < -maxCostExponent || exponent > maxCostExponent {
			return nil
		}
	}
	amount, _ := new(big.Rat).SetString(string(data))
	return amount
}

func roundCostMicros(total *big.Rat) (int64, bool) {
	var scaled, micros, rem big.Int
	scaled.Mul(total.Num(), big.NewInt(1_000_000))
	micros.QuoRem(&scaled, total.Denom(), &rem)
	if rem.Lsh(&rem, 1).Cmp(total.Denom()) >= 0 {
		micros.Add(&micros, big.NewInt(1))
	}
	if !micros.IsInt64() {
		return 0, false
	}
	return micros.Int64(), true
}
