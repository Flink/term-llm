package legacyconfig

import (
	"encoding/json"
	"fmt"
)

// RenameClaudeMoneyRates changes the live rate key without interpreting prices
// or any other settings. It never merges older and newer rate entries.
func RenameClaudeMoneyRates(raw []byte) ([]byte, error) {
	var settings object
	if json.Unmarshal(raw, &settings) != nil {
		return nil, fmt.Errorf("invalid money settings; check the rate overrides for claude-code and claude-bin")
	}
	if settings["rates"] == nil {
		return raw, nil
	}
	var rates []object
	if json.Unmarshal(settings["rates"], &rates) != nil {
		return nil, fmt.Errorf("invalid money rates; check the overrides for claude-code and claude-bin")
	}
	var old []int
	var occupied bool
	for i, rate := range rates {
		var name string
		if field := rate["provider"]; field != nil && json.Unmarshal(field, &name) != nil {
			return nil, fmt.Errorf("invalid money rate provider; check the overrides for claude-code and claude-bin")
		}
		switch name {
		case oldClaude:
			old = append(old, i)
		case newClaude:
			occupied = true
		}
	}
	if len(old) == 0 {
		return raw, nil
	}
	if occupied {
		return nil, fmt.Errorf("money rates exist for both claude-code and claude-bin; remove one set of overrides before upgrading")
	}
	for _, i := range old {
		if err := setField(rates[i], "provider", newClaude); err != nil {
			return nil, err
		}
	}
	if err := setField(settings, "rates", rates); err != nil {
		return nil, err
	}
	return json.Marshal(settings)
}
