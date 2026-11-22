// Package nativeusage represents upstream subscription quotas as independent, provider-defined windows.
package nativeusage

import "time"

// Report is an account snapshot, not activity measured on the proxy.
type Report struct {
	Plan   string  `json:"plan,omitempty"`
	Detail string  `json:"detail,omitempty"`
	Groups []Group `json:"groups"`
}

type Group struct {
	Name    string   `json:"name"`
	Detail  string   `json:"detail,omitempty"`
	Windows []Window `json:"windows"`
}

// Window preserves unknown durations and measurements instead of guessing a quota or forecast.
type Window struct {
	Label           string     `json:"label"`
	DurationMinutes int        `json:"duration_minutes"`
	UsedPercent     float64    `json:"used_percent"`
	PercentUnknown  bool       `json:"percent_unknown,omitempty"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	Detail          string     `json:"detail,omitempty"`
	// LimitReached is set on the affected window or a separate group status row.
	LimitReached bool `json:"limit_reached,omitempty"`
	Disabled     bool `json:"disabled,omitempty"`
}
