package nativeusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DecodeClaude reads the OAuth usage endpoint, not the CLI get_usage envelope.
// Model-specific weekly limits may be returned by name, in model_scoped, or both.
func DecodeClaude(body []byte) (Report, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return Report{}, err
	}
	var report Report
	seen := map[string]int{}
	add := func(group string, w Window) {
		key := strings.ToLower(group) + "/" + w.Label
		if i, ok := seen[key]; ok {
			if report.Groups[i].Windows[0].PercentUnknown && !w.PercentUnknown {
				report.Groups[i].Windows[0] = w
			}
			return
		}
		seen[key] = len(report.Groups)
		report.Groups = append(report.Groups, Group{Name: group, Windows: []Window{w}})
	}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if k == "extra_usage" || k == "model_scoped" {
			continue
		}
		var bucket *claudeWindow
		if err := json.Unmarshal(fields[k], &bucket); err != nil || bucket == nil {
			continue
		}
		group, label, minutes := claudeWindowName(k)
		if group == "" {
			if bucket.Utilization == nil {
				continue
			}
			group, label = strings.ReplaceAll(k, "_", " "), "Usage window"
		}
		w := bucket.window(label, minutes)
		if w.PercentUnknown && bucket.ResetsAt != "" && w.ResetsAt == nil {
			continue // A malformed reset alone is not a usable window.
		}
		add(group, w)
	}
	var scoped []json.RawMessage
	if json.Unmarshal(fields["model_scoped"], &scoped) == nil {
		for i, raw := range scoped {
			var b *claudeWindow
			if json.Unmarshal(raw, &b) != nil || b == nil {
				continue
			}
			w := b.window("Weekly", 10080)
			if name := strings.TrimSpace(b.DisplayName); name != "" {
				add(name, w)
			} else {
				report.Groups = append(report.Groups, Group{Name: fmt.Sprintf("Model limit %d", i+1), Windows: []Window{w}})
			}
		}
	}
	if w, ok := claudeExtraUsage(fields["extra_usage"]); ok {
		add("Extra usage", w)
	}
	if len(report.Groups) == 0 {
		return Report{}, errors.New("Claude did not report quota windows")
	}
	return report, nil
}

type claudeWindow struct {
	DisplayName string   `json:"display_name"`
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

func (b claudeWindow) window(label string, minutes int) Window {
	w := Window{Label: label, DurationMinutes: minutes, PercentUnknown: b.Utilization == nil}
	if reset, err := time.Parse(time.RFC3339Nano, b.ResetsAt); err == nil {
		w.ResetsAt = &reset
	}
	if b.Utilization != nil {
		w.UsedPercent = *b.Utilization
	}
	return w
}

func claudeExtraUsage(raw json.RawMessage) (Window, bool) {
	var extra struct {
		claudeWindow
		Enabled  bool     `json:"is_enabled"`
		Used     *float64 `json:"used_credits"`
		Limit    *float64 `json:"monthly_limit"`
		Currency string   `json:"currency"`
	}
	if json.Unmarshal(raw, &extra) != nil || (!extra.Enabled && extra.Limit == nil && extra.Utilization == nil) {
		return Window{}, false
	}
	w := extra.window("Monthly", 0)
	w.Disabled = !extra.Enabled
	if extra.Used != nil {
		used := claudeMoney(*extra.Used, extra.Currency)
		if extra.Limit != nil {
			w.Detail = used + " of " + claudeMoney(*extra.Limit, extra.Currency)
		} else {
			w.Detail = used + " used"
		}
	}
	return w, true
}

// The CLI receives minor units, except for these whole-unit currencies.
func claudeMoney(amount float64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "" {
		code = "USD"
	}
	symbol := map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "BRL": "R$", "CAD": "CA$", "AUD": "A$", "NZD": "NZ$", "SGD": "S$"}[code]
	if symbol == "" {
		symbol = code + " "
	}
	if code == "JPY" || code == "KRW" || code == "VND" {
		return symbol + strconv.FormatFloat(amount, 'f', 0, 64)
	}
	return symbol + strconv.FormatFloat(amount/100, 'f', 2, 64)
}

func claudeWindowName(key string) (group, label string, minutes int) {
	switch key {
	case "five_hour":
		return "Current session", "5 hours", 300
	case "seven_day":
		return "All models", "Weekly", 10080
	case "seven_day_oauth_apps":
		return "OAuth apps", "Weekly", 10080
	}
	if suffix, ok := strings.CutPrefix(key, "seven_day_"); ok && suffix != "" {
		return strings.ReplaceAll(strings.ToUpper(suffix[:1])+suffix[1:], "_", " "), "Weekly", 10080
	}
	return "", "", 0
}
