package usagetap

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
)

const (
	maxVeniceCostBytes   = 512
	maxVeniceNumberBytes = 64
	maxVeniceExponent    = 64
)

// veniceCharge is independent of token-count evidence. It holds only the last
// event's candidate charge, never the raw event or a sum of cumulative chunks.
type veniceCharge struct {
	micros                int64
	present, bad, sawDone bool
}

// ForVeniceChatCompletions enables usage.cost parsing for an HTTP response.
// The caller must first authenticate the route, response origin and endpoint.
// Only JSON and SSE are supported for the charge (including detected framing).
func ForVeniceChatCompletions(contentType string) *Parser {
	p := ForResponse("/chat/completions", contentType)
	if p.format == JSON || p.format == SSE || p.format == missingContentType {
		p.venice = &veniceCharge{}
	}
	return p
}

func (v *veniceCharge) observe(data []byte, usage json.RawMessage) {
	v.present = false
	if v.bad || v.sawDone || len(usage) == 0 || usage[0] != '{' {
		return
	}
	cost := uniqueJSONField(usage, "cost")
	total := veniceCostTotal(cost)
	if total == nil || uniqueJSONField(data, "usage") == nil {
		return
	}
	v.micros, v.present = roundVeniceMicros(total)
}

// uniqueJSONField avoids the last-key-wins behavior of a map for charge fields.
// The caller has already checked that the enclosing event is valid JSON.
func uniqueJSONField(data []byte, field string) json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(data))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return nil
	}
	var found, value json.RawMessage
	for d.More() {
		key, err := d.Token()
		if err != nil || d.Decode(&value) != nil {
			return nil
		}
		if key == field {
			if found != nil {
				return nil
			}
			found, value = value, nil
		}
	}
	return found
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
		if err != nil || (key != "diem" && key != "usd") || seen[key] {
			return nil
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil
		}
		amount := veniceAmount(value)
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

func veniceAmount(data []byte) *big.Rat {
	// These bytes are a complete JSON value. Only a digit can start an
	// unsigned JSON number; reject strings, null and even signed zero.
	if len(data) == 0 || len(data) > maxVeniceNumberBytes || data[0] < '0' || data[0] > '9' {
		return nil
	}
	// Bound the exponent independently of length before constructing a rational.
	if i := bytes.IndexAny(data, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(string(data[i+1:]))
		if err != nil || exponent < -maxVeniceExponent || exponent > maxVeniceExponent {
			return nil
		}
	}
	amount, _ := new(big.Rat).SetString(string(data))
	return amount
}

func roundVeniceMicros(total *big.Rat) (int64, bool) {
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
