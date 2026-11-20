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
		{"native slot", "claude-bin", accountCredentials{Mode: claudeSharedMode}, true},
		{"per-caller mode removed", "claude-bin", accountCredentials{Mode: "vault"}, false},
		{"missing mode", "claude-bin", accountCredentials{}, false},
		{"setup token removed", "claude-bin", accountCredentials{Mode: "token_only", AccessToken: "synthetic"}, false},
		{"shared rotating login removed", "claude-bin", accountCredentials{Mode: "rotating", AccessToken: "synthetic", RefreshToken: "synthetic-refresh", ExpiresAt: 2000000000}, false},
		{"slot cannot import token", "claude-bin", accountCredentials{Mode: claudeSharedMode, AccessToken: "synthetic"}, false},
		{"slot cannot import refresh", "claude-bin", accountCredentials{Mode: claudeSharedMode, RefreshToken: "synthetic"}, false},
		{"slot cannot import scopes", "claude-bin", accountCredentials{Mode: claudeSharedMode, Scopes: []string{"user:inference"}}, false},
		{"other providers cannot use slot", "chatgpt", accountCredentials{Mode: claudeSharedMode}, false},
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
	rr := adminRequest(s, "POST", "accounts", accountInput{Name: "native", Kind: "claude-bin", Credentials: accountCredentials{Mode: claudeSharedMode}}, cookie, csrf)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var result struct {
		Account AccountInfo `json:"account"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Account.AuthMode != claudeSharedMode || result.Account.State != "needs_login" {
		t.Fatalf("misleading slot metadata: %+v", result.Account)
	}
	route := claudeSetupRoute("native", "native")
	configure(t, s, Config{Routes: []Route{route}}, nil)
	operator := nativeSetupPost(t, s, "claude/setup", cookie, csrf, "connect", route.Name)
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
	if after.identityVersion != before.identityVersion+1 || after.credentials.Mode != claudeSharedMode || after.State != "needs_login" || after.claudeSharedID != before.claudeSharedID {
		t.Fatalf("signout must invalidate sessions but preserve native slot: %+v", after.AccountInfo)
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE caller=?", operator.CallerID) != 0 {
		t.Fatal("sign-out did not invalidate operator enrollment")
	}
}
