package proxy

import (
	"encoding/json"
	"testing"
)

func TestClaudeNativeSlotCredentials(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        string
		credentials accountCredentials
		valid       bool
	}{
		{"native slot", "claude-code", accountCredentials{Mode: "vault"}, true},
		{"missing mode", "claude-code", accountCredentials{}, false},
		{"setup token removed", "claude-code", accountCredentials{Mode: "token_only", AccessToken: "synthetic"}, false},
		{"shared rotating login removed", "claude-code", accountCredentials{Mode: "rotating", AccessToken: "synthetic", RefreshToken: "synthetic-refresh", ExpiresAt: 2000000000}, false},
		{"slot cannot import token", "claude-code", accountCredentials{Mode: "vault", AccessToken: "synthetic"}, false},
		{"slot cannot import refresh", "claude-code", accountCredentials{Mode: "vault", RefreshToken: "synthetic"}, false},
		{"slot cannot import scopes", "claude-code", accountCredentials{Mode: "vault", Scopes: []string{"user:inference"}}, false},
		{"other providers cannot use slot", "chatgpt", accountCredentials{Mode: "vault"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAccountCredentials(tc.kind, tc.credentials)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestClaudeNativeSlotAdminRequiresNoProviderSecrets(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	rr := adminRequest(s, "POST", "accounts", accountInput{Name: "native", Kind: "claude-code", Credentials: accountCredentials{Mode: "vault"}}, cookie, csrf)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var result struct {
		Account AccountInfo `json:"account"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Account.AuthMode != "vault" || result.Account.State != "native_login" {
		t.Fatalf("misleading slot metadata: %+v", result.Account)
	}
	before, err := s.store.account("native")
	if err != nil {
		t.Fatal(err)
	}
	rr = adminRequest(s, "DELETE", "accounts", accountInput{Name: "native"}, cookie, csrf)
	if rr.Code != 200 {
		t.Fatalf("signout: %d %s", rr.Code, rr.Body.String())
	}
	after, err := s.store.account("native")
	if err != nil {
		t.Fatal(err)
	}
	if after.identityVersion != before.identityVersion+1 || after.credentials.Mode != "vault" || after.State != "native_login" {
		t.Fatalf("signout must invalidate sessions but preserve native slot: %+v", after.AccountInfo)
	}
}
