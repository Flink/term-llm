package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeSharedEnrollmentPreservesTypedIdentity(t *testing.T) {
	f := newClaudeForwardProbeFixture(t)
	f.oauth = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "synthetic-vendor-access", "refresh_token": "metadata-real-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": claudeTestScope,
			"account":      map[string]any{"uuid": "account-fixture-uuid", "email_address": "fixture@example.invalid", "unknown_token": "synthetic-vendor-access"},
			"organization": map[string]any{"uuid": "organization-fixture-uuid", "name": "Fixture Organization", "nested": map[string]string{"refresh_token": "metadata-real-refresh"}},
		})
	}
	response := claudeVaultDirect(t, f, claudeVaultCodePayload)
	reply := claudeVaultDecode(t, response)
	if reply.Account == nil || reply.Account.UUID != "account-fixture-uuid" || reply.Account.EmailAddress != "fixture@example.invalid" || reply.Organization == nil || reply.Organization.UUID != "organization-fixture-uuid" || reply.Organization.Name != "Fixture Organization" {
		t.Fatal("native identity metadata dropped", reply.Account, reply.Organization)
	}
	for _, secret := range []string{"synthetic-vendor-access", "metadata-real-refresh", "unknown_token", "nested"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("unknown issuer field or real token escaped whitelist")
		}
	}
}

func TestClaudeVaultIdentityStringsRedactKnownRealTokens(t *testing.T) {
	previous := &claudeVaultRecord{state: "ready", private: claudeVaultPrivate{Access: "fixture-old-access", Refresh: "fixture-old-refresh", Scope: claudeTestScope}}
	body := []byte(`{"access_token":"fixture-next-access","refresh_token":"fixture-next-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:profile user:inference","account":{"uuid":"account/fixture-next-access","email_address":"fixture-next-refresh@example.invalid"},"organization":{"uuid":"organization/fixture-old-access","name":"Organization/fixture-old-refresh"}}`)
	reply, err := parseClaudeVaultReplyForRefresh(body, previous)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Account == nil || reply.Account.UUID != "account/[redacted]" || reply.Account.EmailAddress != "[redacted]@example.invalid" || reply.Organization == nil || reply.Organization.UUID != "organization/[redacted]" || reply.Organization.Name != "Organization/[redacted]" {
		t.Fatal("known current/prior token metadata echo not redacted")
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
