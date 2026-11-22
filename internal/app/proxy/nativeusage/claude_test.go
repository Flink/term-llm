package nativeusage

import (
	"fmt"
	"testing"
)

func TestClaudeQuotaWindows(t *testing.T) {
	report, err := DecodeClaude([]byte(`{"five_hour":{"utilization":0,"resets_at":"2030-01-01T02:00:00Z"},"seven_day":{"utilization":71},"seven_day_oauth_apps":null,"seven_day_opus":{"utilization":12},"another_window":{"utilization":33},"model_scoped":[{"display_name":"Opus","utilization":12},{"display_name":"Other","resets_at":null}],"extra_usage":{"is_enabled":true,"used_credits":12,"monthly_limit":100,"utilization":12}}`))
	if err != nil || len(report.Groups) != 6 {
		t.Fatalf("snapshot %+v: %v", report, err)
	}
	got := map[string]Window{}
	for _, group := range report.Groups {
		got[group.Name] = group.Windows[0]
	}
	if w := got["Current session"]; w.DurationMinutes != 300 || w.PercentUnknown || w.ResetsAt == nil || w.UsedPercent != 0 {
		t.Fatalf("session: %+v", w)
	}
	if w := got["Opus"]; w.DurationMinutes != 10080 || w.UsedPercent != 12 {
		t.Fatalf("model: %+v", w)
	}
	if w := got["another window"]; w.DurationMinutes != 0 || w.UsedPercent != 33 {
		t.Fatalf("unrecognized window: %+v", w)
	}
	if w := got["Other"]; !w.PercentUnknown || w.ResetsAt != nil {
		t.Fatalf("missing value: %+v", w)
	}
	if w := got["Extra usage"]; w.DurationMinutes != 0 || w.Detail != "$0.12 of $1.00" {
		t.Fatalf("extra: %+v", w)
	}
}

func TestClaudeSkipsBreakdownWithoutWindowData(t *testing.T) {
	report, err := DecodeClaude([]byte(`{
		"seven_day": {"utilization": 24},
		"seven_day_breakdown": {"some_model": {"utilization": 8}},
		"seven_day_opus": {"utilization": 0},
		"seven_day_sonnet": {"resets_at": "2030-01-01T02:00:00Z"}
	}`))
	if err != nil || len(report.Groups) != 3 || report.Groups[0].Name != "All models" || report.Groups[1].Name != "Opus" || report.Groups[2].Name != "Sonnet" {
		t.Fatalf("expected only the measured or reset-bearing windows, got %+v: %v", report, err)
	}
}

func TestClaudeInvalidSnapshot(t *testing.T) {
	for _, raw := range []string{`{}`, `{"five_hour":{"resets_at":"not a date"}}`, `{"seven_day":42}`, `{"model_scoped":{}}`, `{"extra_usage":{"is_enabled":false}}`} {
		if _, err := DecodeClaude([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestClaudePartialSnapshot(t *testing.T) {
	const fixture = `{
   "five_hour":{"utilization":17,"resets_at":""},
   "seven_day":{"utilization":42,"resets_at":"not a date"},
   "future_window":{"utilization":{"unsupported":true}},
   "seven_day_opus":{"utilization":null},
   "seven_day_sonnet":{"utilization":18},
   "model_scoped":[{"display_name":"OPUS","utilization":0},{"display_name":"Sonnet"},{"utilization":3},{"utilization":4}]
 }`
	r, err := DecodeClaude([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"Current session": 17, "All models": 42, "Opus": 0, "Sonnet": 18, "Model limit 3": 3, "Model limit 4": 4}
	for _, g := range r.Groups {
		v, ok := want[g.Name]
		if !ok || len(g.Windows) != 1 {
			t.Fatalf("unexpected group: %+v", g)
		}
		w := g.Windows[0]
		if w.PercentUnknown || w.UsedPercent != v || w.ResetsAt != nil {
			t.Errorf("%s: %+v", g.Name, w)
		}
		delete(want, g.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing groups: %v", want)
	}
}

func TestClaudeExtraCurrency(t *testing.T) {
	for _, tt := range []struct{ currency, detail string }{
		{"eur", "€271.40 of €1000000.00"},
		{"JPY", "¥27140 of ¥100000000"},
		{"KRW", "KRW 27140 of KRW 100000000"},
		{"VND", "VND 27140 of VND 100000000"},
		{"CHF", "CHF 271.40 of CHF 1000000.00"},
	} {
		raw := fmt.Sprintf(`{"extra_usage":{"is_enabled":true,"used_credits":27140,"monthly_limit":100000000,"currency":%q}}`, tt.currency)
		r, err := DecodeClaude([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Groups[0].Windows[0].Detail; got != tt.detail {
			t.Errorf("%s: got %q, want %q", tt.currency, got, tt.detail)
		}
	}
}
