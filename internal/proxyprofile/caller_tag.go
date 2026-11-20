package proxyprofile

import (
	"errors"
	"regexp"
	"strings"
)

var (
	callerTagPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	callerTokenPattern = regexp.MustCompile(`^sk-tlp-[0-9a-f]{64}$`)
)

// ValidateCallerTag accepts a 1–64 character attribution label starting with an alphanumeric.
func ValidateCallerTag(tag string) error {
	if !callerTagPattern.MatchString(tag) {
		return errors.New("proxy.tag must be 1–64 characters (letters, digits, '.', '_' or '-'), starting with a letter or digit")
	}
	return nil
}

// TagCallerToken attaches a label to an untagged, proxy-issued client token.
// With no label it leaves all existing token formats unchanged.
func TagCallerToken(token, tag string) (string, error) {
	if tag == "" {
		return token, nil
	}
	if err := ValidateCallerTag(tag); err != nil {
		return "", err
	}
	if !callerTokenPattern.MatchString(token) {
		return "", errors.New("proxy.tag requires an untagged sk-tlp- client token")
	}
	return token + "." + tag, nil
}

// SplitCallerToken separates a label only when both the secret and label are
// canonical. Other token formats, including untagged tokens, pass through unchanged.
func SplitCallerToken(raw string) (secret, tag string) {
	secret, tag, ok := strings.Cut(raw, ".")
	if !ok || !callerTokenPattern.MatchString(secret) || !callerTagPattern.MatchString(tag) {
		return raw, ""
	}
	return secret, tag
}
