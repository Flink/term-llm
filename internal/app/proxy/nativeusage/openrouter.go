package nativeusage

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// DecodeOpenRouter interprets the configured key's /key summary, not usage for
// the entire account. A calendar reset policy alone is not a reset timestamp.
func DecodeOpenRouter(body []byte) (Report, error) {
	var reply struct {
		Data *struct {
			Management *bool    `json:"is_management_key"`
			Limit      *float64 `json:"limit"`
			Remaining  *float64 `json:"limit_remaining"`
			Usage      *float64 `json:"usage"`
			Reset      string   `json:"limit_reset"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return Report{}, errors.New("invalid OpenRouter key summary")
	}
	data := reply.Data
	if data == nil || data.Management == nil || *data.Management {
		return Report{}, errors.New("OpenRouter key usage unavailable")
	}
	if data.Usage == nil || *data.Usage < 0 || data.Limit != nil && *data.Limit < 0 {
		return Report{}, errors.New("invalid OpenRouter key summary")
	}
	w := Window{Label: "Key allowance", PercentUnknown: true}
	switch strings.ToLower(data.Reset) {
	case "daily":
		w.Label, w.DurationMinutes = "Daily", 1440
	case "weekly":
		w.Label, w.DurationMinutes = "Weekly", 10080
	case "monthly":
		w.Label = "Monthly"
	default:
		if data.Reset != "" {
			w.Label = data.Reset
		}
	}
	w.Detail = openRouterDollars(*data.Usage) + " used"
	if data.Limit != nil {
		w.Detail = openRouterDollars(*data.Limit) + " limit"
		if data.Remaining != nil {
			w.Detail += " · " + openRouterDollars(*data.Remaining) + " left"
			if *data.Limit > 0 {
				w.PercentUnknown = false
				w.UsedPercent = 100 * (1 - min(1, max(0, *data.Remaining / *data.Limit)))
			}
		}
	} else {
		w.Detail += " · No key limit"
	}
	return Report{Groups: []Group{{Name: "API key", Windows: []Window{w}}}}, nil
}

func openRouterDollars(value float64) string { return "$" + strconv.FormatFloat(value, 'f', -1, 64) }
