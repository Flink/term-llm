package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeVaultExchangeAndCachedRefreshPreserveTypedIdentity(t *testing.T) {
	f := newClaudeForwardFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		refresh := "metadata-real-refresh"
		if payload["grant_type"] == "refresh_token" {
			if payload["refresh_token"] != "metadata-real-refresh" {
				t.Error("wrong real refresh at issuer")
			}
			refresh = "metadata-next-refresh"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "synthetic-vendor-access", "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": "user:profile user:inference",
			"account":      map[string]any{"uuid": "account-fixture-uuid", "email_address": "fixture@example.invalid", "unknown_token": "synthetic-vendor-access", "nested": map[string]string{"refresh_token": refresh}},
			"organization": map[string]any{"uuid": "organization-fixture-uuid", "name": "Fixture Organization", "unknown_token": refresh, "nested": map[string]string{"access_token": "synthetic-vendor-access"}},
		})
	}
	initialResponse := claudeVaultDirect(t, f, claudeVaultCodePayload)
	initial := claudeVaultDecode(t, initialResponse)
	refreshResponse := claudeVaultDirect(t, f, claudeVaultRefreshPayload(initial.RefreshToken))
	rotated := claudeVaultDecode(t, refreshResponse)
	retryResponse := claudeVaultDirect(t, f, claudeVaultRefreshPayload(initial.RefreshToken))
	retry := claudeVaultDecode(t, retryResponse)
	for _, reply := range []claudeVaultReply{initial, rotated, retry} {
		if reply.Account == nil || reply.Account.UUID != "account-fixture-uuid" || reply.Account.EmailAddress != "fixture@example.invalid" || reply.Organization == nil || reply.Organization.UUID != "organization-fixture-uuid" || reply.Organization.Name != "Fixture Organization" {
			t.Fatal("native identity metadata dropped", reply.Account, reply.Organization)
		}
	}
	for _, body := range []string{initialResponse.Body.String(), refreshResponse.Body.String(), retryResponse.Body.String()} {
		for _, secret := range []string{"synthetic-vendor-access", "metadata-real-refresh", "metadata-next-refresh", "unknown_token", "nested"} {
			if strings.Contains(body, secret) {
				t.Fatal("unknown nested issuer field or real token escaped identity whitelist")
			}
		}
	}
	if retry.AccessToken != rotated.AccessToken || retry.RefreshToken != rotated.RefreshToken || f.oauthCalls.Load() != 2 {
		t.Fatal("metadata preservation bypassed durable cached refresh")
	}
}

func TestClaudeVaultIdentityStringsRedactKnownRealTokens(t *testing.T) {
	f := newClaudeForwardFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		orgAccess, orgRefresh := "synthetic-vendor-access", "synthetic-vendor-refresh"
		if payload["grant_type"] == "authorization_code" {
			orgAccess += "-next"
			orgRefresh += "-next"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "synthetic-vendor-access-next", "refresh_token": "synthetic-vendor-refresh-next", "token_type": "Bearer", "expires_in": 3600, "scope": "user:profile user:inference",
			"account":      map[string]any{"uuid": "account/synthetic-vendor-access-next", "email_address": "synthetic-vendor-refresh-next@example.invalid", "unknown": map[string]string{"token": "synthetic-vendor-access-next"}},
			"organization": map[string]string{"uuid": "organization/" + orgAccess, "name": "Organization/" + orgRefresh},
		})
	}
	first := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	reply := claudeVaultDecode(t, first)
	cached := claudeVaultDirect(t, f, claudeVaultRefreshPayload(f.refresh))
	retry := claudeVaultDecode(t, cached)
	for _, result := range []claudeVaultReply{reply, retry} {
		if result.Account == nil || result.Account.UUID != "account/[redacted]" || result.Account.EmailAddress != "[redacted]@example.invalid" || result.Organization == nil || result.Organization.UUID != "organization/[redacted]" || result.Organization.Name != "Organization/[redacted]" {
			t.Fatal("known current/prior token metadata echo not redacted")
		}
	}
	for _, body := range []string{first.Body.String(), cached.Body.String()} {
		if strings.Contains(body, "synthetic-vendor") || strings.Contains(body, "unknown") {
			t.Fatal("known real token escaped metadata redaction")
		}
	}
	if f.oauthCalls.Load() != 1 {
		t.Fatal("sanitized metadata receipt not cached")
	}
	// Code exchange has no prior refresh identity, but must redact its real pair.
	exchanged := claudeVaultDecode(t, claudeVaultDirect(t, f, claudeVaultCodePayload))
	if exchanged.Account == nil || exchanged.Account.UUID != "account/[redacted]" || exchanged.Account.EmailAddress != "[redacted]@example.invalid" {
		t.Fatal("code exchange metadata leaked its real pair")
	}
}

func TestClaudeVaultIdentityMetadataStringsAreBounded(t *testing.T) {
	for _, field := range []string{"account_uuid", "account_email", "organization_uuid", "organization_name"} {
		t.Run(field, func(t *testing.T) {
			account := map[string]any{"uuid": "account-fixture", "email_address": "fixture@example.invalid"}
			organization := map[string]any{"uuid": "organization-fixture", "name": "Fixture"}
			oversized := strings.Repeat("x", claudeVaultMetadataMaxString+1)
			switch field {
			case "account_uuid":
				account["uuid"] = oversized
			case "account_email":
				account["email_address"] = oversized
			case "organization_uuid":
				organization["uuid"] = oversized
			case "organization_name":
				organization["name"] = oversized
			}
			body, _ := json.Marshal(map[string]any{"access_token": "real-access", "refresh_token": "real-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "user:profile user:inference", "account": account, "organization": organization})
			if _, err := parseClaudeVaultReply(body); err == nil {
				t.Fatal("unbounded native identity metadata accepted", field)
			}
		})
	}
}
