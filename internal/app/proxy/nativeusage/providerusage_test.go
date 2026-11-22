package nativeusage

import "testing"

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
