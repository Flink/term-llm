package proxyprofile

import (
	"strings"
	"testing"
)

func TestCallerTagRoundTrip(t *testing.T) {
	secret := "sk-tlp-" + strings.Repeat("0123456789abcdef", 4)
	for _, tag := range []string{"", "abcd", "2", "A1.b_c-d", strings.Repeat("a", 64)} {
		t.Run(tag, func(t *testing.T) {
			raw, err := TagCallerToken(secret, tag)
			if err != nil {
				t.Fatal(err)
			}
			want := secret
			if tag != "" {
				want += "." + tag
			}
			gotSecret, gotTag := SplitCallerToken(raw)
			if raw != want || gotSecret != secret || gotTag != tag {
				t.Fatal("caller token did not round-trip")
			}
		})
	}
}

func TestCallerTagRejectsInvalidTags(t *testing.T) {
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	if ValidateCallerTag("") == nil {
		t.Fatal("empty is not a valid attribution label")
	}
	for _, tag := range []string{".a", "-a", "_a", "é", "a b", "a/b", "a\n", strings.Repeat("a", 65)} {
		t.Run(tag, func(t *testing.T) {
			if ValidateCallerTag(tag) == nil {
				t.Fatal("invalid tag accepted")
			}
			if _, err := TagCallerToken(secret, tag); err == nil {
				t.Fatal("invalid tag appended")
			}
			raw := secret + "." + tag
			if got, label := SplitCallerToken(raw); got != raw || label != "" {
				t.Fatal("invalid label split from token")
			}
		})
	}
}

func TestCallerTagPreservesNonCanonicalTokens(t *testing.T) {
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	for name, token := range map[string]string{
		"uppercase hex": "sk-tlp-" + strings.Repeat("A", 64),
		"non-hex":       "sk-tlp-" + strings.Repeat("g", 64),
		"short":         secret[:len(secret)-1],
		"long":          secret + "a",
		"other prefix":  "admin-" + strings.Repeat("a", 64),
		"embedded dot":  "opaque.token",
		"leading space": " " + secret,
		"trailing dot":  secret + ".",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := TagCallerToken(token, ""); err != nil || got != token {
				t.Fatal("untagged token was not preserved")
			}
			if _, err := TagCallerToken(token, "abcd"); err == nil {
				t.Fatal("non-canonical token was accepted for tagging")
			}
			raw := token + ".abcd"
			if got, tag := SplitCallerToken(raw); got != raw || tag != "" {
				t.Fatal("non-canonical token was split")
			}
		})
	}
	if _, err := TagCallerToken(secret+".first", "second"); err == nil {
		t.Fatal("already-tagged token accepted for tagging")
	}
}
