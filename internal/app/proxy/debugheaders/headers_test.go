package debugheaders

import "testing"

func TestBearerPreview(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"bearer", []string{"Bearer oc_sk_123456789"}, "Bearer oc_…"},
		{"mixed case", []string{"bearer sk-tlp-123456789"}, "Bearer sk-…"},
		{"short", []string{"Bearer abc"}, "Bearer [redacted]"},
		{"missing key", []string{"Bearer "}, "[redacted]"},
		{"malformed", []string{"Bearer invalid token value"}, "Bearer [redacted]"},
		{"basic", []string{"Basic dXNlcjpwYXNzd29yZA=="}, "[redacted]"},
		{"duplicate", []string{"Bearer oc_sk_123456789", "Bearer oc_sk_987654321"}, "[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := debugBearerPreview(tc.values); got != tc.want {
				t.Fatalf("preview = %q; want %q", got, tc.want)
			}
		})
	}
}
