package proxyclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	agySharedTestAccess  = "agy-access-tlv2-" + strings.Repeat("a", 42) + "A"
	agySharedTestRefresh = "agy-refresh-tlv2-" + strings.Repeat("b", 42) + "A"
)

func TestAgySharedDiscoveryAliases(t *testing.T) {
	valid := `"access_placeholder":"` + agySharedTestAccess + `","refresh_placeholder":"` + agySharedTestRefresh + `","expires_in":3600`
	for _, tc := range []struct {
		name, fields, wantErr string
		shared                bool
	}{
		{"alias-free operator", "", "", false},
		{"shared without scopes", valid, "", true},
		{"shared with scopes", valid + `,"scopes":"openid https://www.googleapis.com/auth/cloud-platform"`, "", true},
		{"partial aliases", `"refresh_placeholder":"` + agySharedTestRefresh + `"`, "access_placeholder", false},
		{"lifetime only", `"expires_in":3600`, "access_placeholder", false},
		{"vendor access", strings.Replace(valid, agySharedTestAccess, "ya29.not-an-alias", 1), "access_placeholder", false},
		{"vendor refresh", strings.Replace(valid, agySharedTestRefresh, "1//not-an-alias", 1), "refresh_placeholder", false},
		{"old hex alias", strings.Replace(valid, agySharedTestAccess, "agy-access-tlv2-"+strings.Repeat("a", 64), 1), "access_placeholder", false},
		{"noncanonical alias", strings.Replace(valid, agySharedTestAccess, strings.TrimSuffix(agySharedTestAccess, "A")+"B", 1), "access_placeholder", false},
		{"missing lifetime", strings.TrimSuffix(valid, `,"expires_in":3600`), "expires_in", false},
		{"zero lifetime", strings.Replace(valid, `3600`, `0`, 1), "expires_in", false},
		{"excessive lifetime", strings.Replace(valid, `3600`, `3601`, 1), "expires_in", false},
		{"quoted lifetime", strings.Replace(valid, `3600`, `"3600"`, 1), "expires_in", false},
		{"duplicate alias", valid + `,"access_placeholder":"example-secret"`, "invalid", false},
		{"null scope", valid + `,"scopes":null`, "scopes", false},
		{"incorrectly typed scope", valid + `,"scopes":["openid"]`, "scopes", false},
		{"malformed scopes", valid + `,"scopes":"openid\nprofile"`, "scopes", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAgySharedIdentity([]byte("{" + tc.fields + "}"))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "not-an-alias") || strings.Contains(err.Error(), "example-secret") {
					t.Fatalf("unsafe acceptance or discovery error: %v", err)
				}
				return
			}
			if err != nil || (AgyNativeProxy{identity: got}).HasSharedIdentity() != tc.shared {
				t.Fatalf("incorrect discovery mode: %v", err)
			}
		})
	}
}

func TestAgySharedSeedNativeEnvelope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only native storage")
	}
	home := t.TempDir()
	metadata := `{"access_placeholder":"` + agySharedTestAccess + `","refresh_placeholder":"` + agySharedTestRefresh + `","expires_in":3600,"scopes":"oidc-canary","vendor_credential":"vendor-canary"}`
	identity, err := parseAgySharedIdentity([]byte(metadata))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", 3600))
	if err := identity.seed(home, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", agyCredentialFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		AuthMethod string         `json:"auth_method"`
		Token      map[string]any `json:"token"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.AuthMethod != "consumer" || len(envelope.Token) != 5 || envelope.Token["access_token"] != agySharedTestAccess || envelope.Token["refresh_token"] != agySharedTestRefresh || envelope.Token["token_type"] != "Bearer" || envelope.Token["expires_in"] != 3600.0 || envelope.Token["expiry"] != "2030-01-02T03:04:05Z" {
		t.Fatal("seed did not match the native OAuth envelope")
	}
	if strings.Contains(string(data), "canary") {
		t.Fatal("discovery metadata outside the native envelope reached the private profile")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("missing private Antigravity token file", err)
	}
}
