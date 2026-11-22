package nativeusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type agyQuotaSummary struct {
	Description string           `json:"description"`
	Groups      []*agyQuotaGroup `json:"groups"`
	Buckets     json.RawMessage  `json:"buckets"`
}

type agyQuotaGroup struct {
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	Buckets     []*agyQuotaBucket `json:"buckets"`
}

type agyQuotaBucket struct {
	ID                string       `json:"bucketId"`
	DisplayName       string       `json:"displayName"`
	Description       string       `json:"description"`
	Window            string       `json:"window"`
	RemainingFraction *json.Number `json:"remainingFraction"`
	RemainingAmount   *json.Number `json:"remainingAmount"`
	ResetTime         string       `json:"resetTime"`
	Disabled          bool         `json:"disabled"`
}

// DecodeAgy parses retrieveUserQuotaSummary's protoJSON response. Grouped buckets
// supersede the older flat list; the latter is used only as a fallback.
func DecodeAgy(body []byte) (Report, error) {
	var src agyQuotaSummary
	if err := json.Unmarshal(body, &src); err != nil {
		// Do not echo upstream bodies or field values in errors.
		return Report{}, errors.New("invalid Antigravity quota summary JSON")
	}
	out := Report{Detail: strings.TrimSpace(src.Description)}
	var hasBuckets bool
	for i, raw := range src.Groups {
		if raw == nil {
			return Report{}, fmt.Errorf("Antigravity quota group %d is null", i+1)
		}
		group, err := decodeAgyQuotaGroup(*raw, i)
		if err != nil {
			return Report{}, err
		}
		hasBuckets = hasBuckets || len(group.Windows) != 0
		out.Groups = append(out.Groups, group)
	}
	if hasBuckets {
		return out, nil
	}
	var buckets []*agyQuotaBucket
	if len(src.Buckets) != 0 {
		if err := json.Unmarshal(src.Buckets, &buckets); err != nil {
			return Report{}, errors.New("invalid Antigravity legacy quota buckets JSON")
		}
	}
	if len(buckets) == 0 {
		return Report{}, errors.New("Antigravity quota summary contains no buckets")
	}
	group, err := decodeAgyQuotaGroup(agyQuotaGroup{DisplayName: "Quota", Buckets: buckets}, len(out.Groups))
	if err != nil {
		return Report{}, err
	}
	out.Groups = append(out.Groups, group)
	return out, nil
}

func decodeAgyQuotaGroup(src agyQuotaGroup, index int) (Group, error) {
	out := Group{
		Name:    strings.TrimSpace(src.DisplayName),
		Detail:  strings.TrimSpace(src.Description),
		Windows: make([]Window, 0, len(src.Buckets)),
	}
	if out.Name == "" {
		out.Name = fmt.Sprintf("Quota group %d", index+1)
	}
	for i, b := range src.Buckets {
		if b == nil {
			return Group{}, fmt.Errorf("Antigravity quota group %d bucket %d is null", index+1, i+1)
		}
		window, err := decodeAgyQuotaBucket(*b)
		if err != nil {
			return Group{}, fmt.Errorf("Antigravity quota group %d bucket %d: %w", index+1, i+1, err)
		}
		out.Windows = append(out.Windows, window)
	}
	return out, nil
}

func decodeAgyQuotaBucket(b agyQuotaBucket) (Window, error) {
	label, minutes, name := agyQuotaBucketLabel(b)
	out := Window{
		Label: label, DurationMinutes: minutes,
		PercentUnknown: b.RemainingFraction == nil, Disabled: b.Disabled,
	}
	if b.ResetTime != "" {
		reset, err := time.Parse(time.RFC3339Nano, b.ResetTime)
		if err != nil {
			return Window{}, errors.New("invalid reset time")
		}
		out.ResetsAt = &reset
	}
	var amount string
	switch {
	case b.RemainingFraction != nil && b.RemainingAmount != nil:
		return Window{}, errors.New("both remaining fraction and remaining amount are set")
	case b.RemainingFraction != nil:
		value, err := b.RemainingFraction.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return Window{}, errors.New("invalid remaining fraction")
		}
		out.UsedPercent = (1 - min(1, max(0, value))) * 100
	case b.RemainingAmount != nil:
		value, err := b.RemainingAmount.Int64()
		if err != nil {
			return Window{}, errors.New("invalid remaining amount")
		}
		amount = strconv.FormatInt(value, 10) + " remaining"
	}
	var details []string
	for _, part := range []string{name, strings.TrimSpace(b.Description), amount} {
		if part != "" && !strings.EqualFold(part, label) && !agyContainsFold(details, part) {
			details = append(details, part)
		}
	}
	out.Detail = strings.Join(details, " · ")
	return out, nil
}

func agyContainsFold(parts []string, text string) bool {
	for _, part := range parts {
		if strings.EqualFold(part, text) {
			return true
		}
	}
	return false
}

func agyQuotaBucketLabel(b agyQuotaBucket) (label string, minutes int, detailName string) {
	name := strings.TrimSpace(b.DisplayName)
	if name == "" {
		name = strings.TrimSpace(b.ID)
	}
	window := strings.TrimSpace(b.Window)
	if minutes := agyWindowMinutes(window); minutes != 0 {
		// A generic label such as "Weekly Limit Remaining" just repeats the
		// duration. Model-specific bucket names still belong in the detail.
		nameWindow := strings.ToLower(name)
		for _, suffix := range []string{" remaining", " limit", " quota", " window"} {
			nameWindow = strings.TrimSuffix(nameWindow, suffix)
		}
		if agyWindowMinutes(nameWindow) == minutes {
			name = ""
		}
		return agyDurationLabel(minutes), minutes, name
	}
	if window != "" {
		return window, 0, name
	}
	if name != "" {
		return name, 0, ""
	}
	return "Usage window", 0, ""
}

const maxAgyWindowMinutes = 366 * 24 * 60

func agyWindowMinutes(window string) int {
	window = strings.ToLower(window)
	switch window {
	case "hourly":
		return 60
	case "daily":
		return 24 * 60
	case "week", "weekly":
		return 7 * 24 * 60
	}
	if d, err := time.ParseDuration(window); err == nil && d > 0 && d%time.Minute == 0 && d/time.Minute <= maxAgyWindowMinutes {
		return int(d / time.Minute)
	}
	// Protocol windows are strings, not enums. Accept explicit whole units
	// (including days and weeks), but don't infer a length for monthly limits.
	n, unit := splitAgyWindow(window)
	var unitMinutes int
	switch unit {
	case "m", "min", "mins", "minute", "minutes":
		unitMinutes = 1
	case "h", "hr", "hrs", "hour", "hours":
		unitMinutes = 60
	case "d", "day", "days":
		unitMinutes = 24 * 60
	case "w", "wk", "wks", "week", "weeks":
		unitMinutes = 7 * 24 * 60
	}
	if unitMinutes == 0 || n <= 0 || n > maxAgyWindowMinutes/unitMinutes {
		return 0
	}
	return n * unitMinutes
}

func splitAgyWindow(window string) (int, string) {
	i := strings.IndexFunc(window, func(r rune) bool { return r < '0' || r > '9' })
	if i > 0 {
		n, _ := strconv.Atoi(window[:i])
		return n, strings.TrimLeft(window[i:], " _-")
	}
	for _, word := range []struct {
		prefix string
		value  int
	}{{"one", 1}, {"five", 5}, {"six", 6}} {
		if tail, ok := strings.CutPrefix(window, word.prefix); ok && tail != "" && strings.ContainsRune(" _-", rune(tail[0])) {
			return word.value, strings.TrimLeft(tail, " _-")
		}
	}
	return 0, ""
}

func agyDurationLabel(minutes int) string {
	for _, unit := range []struct {
		name    string
		minutes int
	}{{"week", 7 * 24 * 60}, {"day", 24 * 60}, {"hour", 60}, {"minute", 1}} {
		if minutes%unit.minutes == 0 {
			return fmt.Sprintf("%d %s window", minutes/unit.minutes, unit.name)
		}
	}
	return "Usage window"
}
