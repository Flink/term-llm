package providerusage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSparseProviderWindows(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		decode     func([]byte) (*Report, error)
	}{
		{"ChatGPT", `{"rate_limit":{"primary_window":{"used_percent":null,"limit_window_seconds":604800,"reset_at":1893456000}},"additional_rate_limits":[{"rate_limit":{"primary_window":{},"secondary_window":{"used_percent":0}}}]}`, DecodeChatGPT},
		{"OpenCode Go", `{"usage":{"weekly":{"resetsAt":"2030-01-01T00:00:00Z"},"monthly":{"percent":0}}}`, DecodeOpenCodeGo},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report, err := tt.decode([]byte(tt.body))
			if err != nil || len(report.Limits) != 2 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			unknown := report.Limits[0].PrimaryWindow
			if unknown == nil || !unknown.PercentUnknown || unknown.DurationMinutes != 10080 || unknown.ResetsAt.Unix() != 1893456000 {
				t.Fatalf("unknown window: %+v", unknown)
			}
			formatted := Format(report, time.Unix(1893456000-86400, 0))
			if !strings.Contains(formatted, "Usage unknown") || strings.Count(formatted, "0% used") != 1 || strings.Contains(formatted, "Projected") {
				t.Fatalf("unexpected output: %s", formatted)
			}
			data, err := json.Marshal(report)
			if err != nil || !strings.Contains(string(data), `"used_percent":null`) || !strings.Contains(string(data), `"used_percent":0`) {
				t.Fatalf("json=%s err=%v", data, err)
			}
		})
	}
}

func TestDecodeGrokRejectsAbsentMeasurement(t *testing.T) {
	if _, err := DecodeGrok([]byte(`{"config":{}}`)); err == nil {
		t.Fatal("empty configuration accepted")
	}
}
