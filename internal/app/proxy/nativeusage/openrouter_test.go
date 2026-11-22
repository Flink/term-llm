package nativeusage

import "testing"

func TestOpenRouterKeyLimits(t *testing.T) {
	for _, tt := range []struct {
		body        string
		ok, unknown bool
		used        float64
	}{
		{`{"data":{"is_management_key":false,"usage":200,"limit":10,"limit_remaining":2,"limit_reset":"weekly"}}`, true, false, 80},
		{`{"data":{"is_management_key":false,"usage":23,"limit":null}}`, true, true, 0},
		{`{"data":{"is_management_key":true,"usage":0}}`, false, false, 0},
		{`{"data":{}}`, false, false, 0},
	} {
		r, err := DecodeOpenRouter([]byte(tt.body))
		if (err == nil) != tt.ok {
			t.Errorf("decode %s: %v", tt.body, err)
			continue
		}
		if tt.ok {
			w := r.Groups[0].Windows[0]
			if w.PercentUnknown != tt.unknown || w.UsedPercent != tt.used || w.ResetsAt != nil {
				t.Errorf("window %+v", w)
			}
		}
	}
}

func TestOpenRouterDisplay(t *testing.T) {
	for _, tt := range []struct {
		body, label, detail string
		minutes             int
	}{
		{`{"data":{"is_management_key":false,"usage":0.000001,"limit_reset":"daily"}}`, "Daily", "$0.000001 used · No key limit", 1440},
		{`{"data":{"is_management_key":false,"usage":0,"limit":1000000,"limit_remaining":1e-7,"limit_reset":"WEEKLY"}}`, "Weekly", "$1000000 limit · $0.0000001 left", 10080},
		{`{"data":{"is_management_key":false,"usage":0,"limit_reset":"monthly"}}`, "Monthly", "$0 used · No key limit", 0},
	} {
		r, err := DecodeOpenRouter([]byte(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		w := r.Groups[0].Windows[0]
		if w.Label != tt.label || w.DurationMinutes != tt.minutes || w.Detail != tt.detail {
			t.Errorf("%s: %+v", tt.body, w)
		}
	}
}
