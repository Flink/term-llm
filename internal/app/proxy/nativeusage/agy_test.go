package nativeusage

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDecodeAgyGroupedQuota(t *testing.T) {
	body := `{
	  "description": "Models within a group share limits.",
	  "futureField": {"value": 3},
	  "groups": [
	    {"displayName":"Gemini Models", "description":"Gemini Pro and Flash", "buckets":[
	      {"bucketId":"gemini-5h", "displayName":"Five Hour Limit Remaining", "window":"5h",
	       "remainingFraction":0.25, "resetTime":"2026-09-01T13:04:05.123456789Z"},
	      {"bucketId":"gemini-weekly", "displayName":"Weekly Limit Remaining", "window":"weekly",
	       "remainingFraction":0.75},
	      {"bucketId":"image", "displayName":"Image generations", "description":"Image generations",
	       "window":"Calendar month", "remainingAmount":"9007199254740993"}
	    ]},
	    {"displayName":"Claude and GPT Models", "buckets":[
	      {"bucketId":"claude-weekly", "displayName":"Claude weekly", "window":"7d", "remainingFraction":1},
	      {"bucketId":"gpt-weekly", "displayName":"GPT weekly", "window":"7d", "remainingFraction":0},
	      {"bucketId":"third-party-6h", "displayName":"Six Hour Limit Remaining", "window":"6h",
	       "remainingFraction":null, "resetTime":"2026-09-02T01:00:00Z", "disabled":true},
	      {"bucketId":"request-cap", "displayName":"Request cap", "description":"Daily request allowance",
	       "remainingAmount":"0"}
	    ]},
	    {"displayName":"Experimental Models", "description":"No quotas reported yet"}
	  ],
	  "buckets": [{"bucketId":"old-copy", "remainingFraction":0.1}]
	}`
	geminiReset := time.Date(2026, 9, 1, 13, 4, 5, 123456789, time.UTC)
	thirdPartyReset := time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)
	want := Report{Detail: "Models within a group share limits.", Groups: []Group{
		{Name: "Gemini Models", Detail: "Gemini Pro and Flash", Windows: []Window{
			{Label: "5 hour window", DurationMinutes: 300, UsedPercent: 75, ResetsAt: &geminiReset},
			{Label: "1 week window", DurationMinutes: 10080, UsedPercent: 25},
			{Label: "Calendar month", PercentUnknown: true, Detail: "Image generations · 9007199254740993 remaining"},
		}},
		{Name: "Claude and GPT Models", Windows: []Window{
			{Label: "1 week window", DurationMinutes: 10080, Detail: "Claude weekly"},
			{Label: "1 week window", DurationMinutes: 10080, UsedPercent: 100, Detail: "GPT weekly"},
			{Label: "6 hour window", DurationMinutes: 360, PercentUnknown: true, ResetsAt: &thirdPartyReset, Disabled: true},
			{Label: "Request cap", PercentUnknown: true, Detail: "Daily request allowance · 0 remaining"},
		}},
		{Name: "Experimental Models", Detail: "No quotas reported yet", Windows: []Window{}},
	}}
	assertAgyQuota(t, body, want)
}

func TestDecodeAgyLegacyQuota(t *testing.T) {
	body := `{"groups":[],"buckets":[
	  {"bucketId":"legacy-5h", "window":"five_hour", "remainingFraction":1.1, "remainingAmount":null},
	  {"bucketId":"legacy-requests", "description":"Requests on demand", "remainingAmount":9223372036854775807},
	  {"bucketId":"legacy-model", "window":"rolling preview", "remainingFraction":-0.1, "disabled":true}
	]}`
	assertAgyQuota(t, body, Report{Groups: []Group{{Name: "Quota", Windows: []Window{
		{Label: "5 hour window", DurationMinutes: 300, Detail: "legacy-5h"},
		{Label: "legacy-requests", PercentUnknown: true, Detail: "Requests on demand · 9223372036854775807 remaining"},
		{Label: "rolling preview", UsedPercent: 100, Disabled: true, Detail: "legacy-model"},
	}}}})

	// Empty groups still carry metadata, but have no quotas to supersede legacy buckets.
	assertAgyQuota(t, `{"groups":[{"description":"No model labels"}],"buckets":[{"bucketId":"legacy"}]}`,
		Report{Groups: []Group{
			{Name: "Quota group 1", Detail: "No model labels", Windows: []Window{}},
			{Name: "Quota", Windows: []Window{{Label: "legacy", PercentUnknown: true}}},
		}})
}

func TestDecodeAgyWindowDurations(t *testing.T) {
	for _, tt := range []struct {
		window  string
		minutes int
		label   string
	}{
		{" five-hour ", 300, "5 hour window"},
		{"Six Hour", 360, "6 hour window"},
		{"7d", 10080, "1 week window"},
		{"2 Weeks", 20160, "2 week window"},
		{"36h", 2160, "36 hour window"},
		{"90 minutes", 90, "90 minute window"},
		{"1h30m", 90, "90 minute window"},
		{"1.5h", 90, "90 minute window"},
		{"daily", 1440, "1 day window"},
		{"366d", 527040, "366 day window"},
		{"367d", 0, "367d"},
		{"99999999999999999999999w", 0, "99999999999999999999999w"},
		{"45s", 0, "45s"},
		{"-6h", 0, "-6h"},
		{"monthly", 0, "monthly"},
		{"1 month", 0, "1 month"},
		{"Rolling Event Limit", 0, "Rolling Event Limit"},
	} {
		t.Run(tt.window, func(t *testing.T) {
			assertAgyQuota(t, `{"buckets":[{"window":"`+tt.window+`","remainingFraction":"0.5"}]}`,
				Report{Groups: []Group{{Name: "Quota", Windows: []Window{{Label: tt.label, DurationMinutes: tt.minutes, UsedPercent: 50}}}}})
		})
	}
}

func TestDecodeAgyInvalidQuota(t *testing.T) {
	for _, tt := range []struct {
		name, body, errorText string
	}{
		{"malformed", `{"groups":`, "summary JSON"},
		{"wrong root", `[]`, "summary JSON"},
		{"trailing JSON", `{"buckets":[{"window":"5h"}]} {}`, "summary JSON"},
		{"upstream error envelope", `{"error":{"message":"private upstream detail"}}`, "no buckets"},
		{"empty", `{"groups":[{"displayName":"Gemini"}],"buckets":[]}`, "no buckets"},
		{"legacy wrong shape", `{"buckets":{}}`, "legacy quota buckets JSON"},
		{"null group", `{"groups":[null]}`, "group 1 is null"},
		{"null bucket", `{"buckets":[null]}`, "group 1 bucket 1 is null"},
		{"reset wrong shape", `{"groups":[{"buckets":[{"resetTime":{"seconds":"10"}}]}]}`, "summary JSON"},
		{"invalid reset", `{"groups":[{"buckets":[{"resetTime":"private upstream detail"}]}]}`, "group 1 bucket 1: invalid reset time"},
		{"ambiguous oneof", `{"buckets":[{"remainingFraction":0,"remainingAmount":"0"}]}`, "both remaining fraction and remaining amount"},
		{"overflow amount", `{"buckets":[{"remainingAmount":"9223372036854775808"}]}`, "invalid remaining amount"},
		{"noninteger amount", `{"buckets":[{"remainingAmount":"2.5"}]}`, "invalid remaining amount"},
		{"overflow fraction", `{"buckets":[{"remainingFraction":1e999}]}`, "invalid remaining fraction"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeAgy([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.errorText) || strings.Contains(err.Error(), "private upstream detail") {
				t.Fatalf("error = %v; want %q with no upstream detail", err, tt.errorText)
			}
			if !reflect.DeepEqual(got, Report{}) {
				t.Fatalf("returned partial report on error: %#v", got)
			}
		})
	}
}

func assertAgyQuota(t *testing.T, body string, want Report) {
	t.Helper()
	got, err := DecodeAgy([]byte(body))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeAgy() = %#v, %v; want %#v", got, err, want)
	}
}
