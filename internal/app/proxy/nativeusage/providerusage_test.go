package nativeusage

import (
	"fmt"
	"testing"
)

func TestChatGPTCreditBalanceDisplay(t *testing.T) {
	for _, tt := range []struct {
		balance string
		want    string
	}{
		{"62003.2882720000", "62003.28"},
		{"12.50", "12.50"},
		{"5", "5"},
		{"unknown", "unknown"},
	} {
		t.Run(tt.balance, func(t *testing.T) {
			body := fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":0}},"credits":{"has_credits":true,"balance":%q}}`, tt.balance)
			report, err := DecodeChatGPT([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if want := tt.want + " credits remaining"; report.Detail != want {
				t.Errorf("detail = %q, want %q", report.Detail, want)
			}
		})
	}
}

func TestChatGPTGroupStatusAndSparseWindows(t *testing.T) {
	report, err := DecodeChatGPT([]byte(`{"rate_limit":{"limit_reached":true,"primary_window":{"limit_window_seconds":18000},"secondary_window":{"used_percent":0}},"additional_rate_limits":[{"limit_name":"Denied","rate_limit":{"allowed":false}},{"limit_name":"Empty","rate_limit":{}}],"rate_limit_reached_type":{"type":"weekly_limit"},"credits":{"has_credits":false,"balance":"0.00"}}`))
	if err != nil || len(report.Groups) != 2 || report.Detail != "weekly limit" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	ws := report.Groups[0].Windows
	if len(ws) != 3 || !ws[0].PercentUnknown || ws[0].DurationMinutes != 300 || ws[0].LimitReached || ws[1].PercentUnknown || ws[1].UsedPercent != 0 || ws[1].LimitReached || !ws[2].LimitReached || !ws[2].PercentUnknown {
		t.Fatalf("windows=%+v", ws)
	}
	ws = report.Groups[1].Windows
	if len(ws) != 1 || !ws[0].LimitReached || !ws[0].PercentUnknown {
		t.Fatalf("denied windows=%+v", ws)
	}
}

func TestOpenCodeGoWindowStatus(t *testing.T) {
	report, err := DecodeOpenCodeGo([]byte(`{"usage":{"rolling":{"status":"rate-limited","resetsAt":"2030-01-01T00:00:00Z"},"monthly":{"percent":8}}}`))
	if err != nil || len(report.Groups) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	w := report.Groups[0].Windows[0]
	if !w.LimitReached || !w.PercentUnknown || w.ResetsAt == nil || w.DurationMinutes != 300 {
		t.Fatalf("rolling=%+v", w)
	}
	w = report.Groups[1].Windows[0]
	if w.LimitReached || w.PercentUnknown || w.UsedPercent != 8 {
		t.Fatalf("monthly=%+v", w)
	}
}

func TestChatGPTUnmeasuredSiblingDoesNotImplicateMeasuredWindow(t *testing.T) {
	report, err := DecodeChatGPT([]byte(`{"rate_limit":{"allowed":false,"primary_window":{},"secondary_window":{"used_percent":2}}}`))
	if err != nil || len(report.Groups) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	ws := report.Groups[0].Windows
	if len(ws) != 3 || !ws[0].PercentUnknown || ws[0].LimitReached || ws[1].LimitReached || !ws[2].LimitReached {
		t.Fatalf("windows=%+v", ws)
	}
}
