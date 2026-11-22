package proxy

import (
	"context"
	"strings"
	"testing"
)

func TestDeviceReconnectMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, kind, id, digest string
		operator               accountOperator
		want                   string
	}{
		{"pre-upgrade signed-out", "grok", "", "", accountOperator{}, "missing_identity"},
		{"signed-out pinned", "grok", "", "private-digest", accountOperator{}, "eligible"},
		{"chatgpt legacy pin", "chatgpt", "", "private-digest", accountOperator{}, "eligible"},
		{"live identity", "grok", "private-id", "", accountOperator{}, "eligible"},
		{"issuer", "chatgpt", "private-id", "", accountOperator{Issuer: "https://private-issuer.invalid"}, "custom_settings"},
		{"origin", "grok", "private-id", "", accountOperator{Origins: []string{"https://private-origin.invalid"}}, "custom_settings"},
		{"client", "grok", "private-id", "", accountOperator{ClientID: "private-client"}, "custom_settings"},
		{"unrelated kind", "copilot", "", "", accountOperator{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			admin, csrf := loginAdmin(t, s)
			a := accountRecord{AccountInfo: AccountInfo{Name: "work", Kind: tc.kind, State: "needs_login", Generation: 2}, identityHash: tc.digest, credentials: accountCredentials{AccountID: tc.id}, operator: tc.operator}
			if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
				t.Fatal(err)
			}
			w := adminRequest(s, "GET", "accounts", nil, admin, "")
			got := readChatGPTSetupReply[struct {
				Accounts []map[string]any `json:"accounts"`
			}](t, w, 200)
			if len(got.Accounts) != 1 || strings.Contains(w.Body.String(), "private-") {
				t.Fatal("unexpected metadata or private fields disclosed")
			}
			v, exists := got.Accounts[0]["device_reconnect"]
			if exists != (tc.want != "") || exists && v != tc.want {
				t.Fatalf("device_reconnect=%v; want %q", v, tc.want)
			}
			if tc.want == "custom_settings" || tc.want == "missing_identity" {
				s.opts.ConfigFile = "file-managed"
				w = adminRequest(s, "POST", tc.kind+"/setup", deviceSetupInput{Account: "work"}, admin, csrf)
				if w.Code != 409 || (tc.want == "missing_identity" && !strings.Contains(w.Body.String(), "operator")) {
					t.Fatalf("reconnect: %d %s", w.Code, w.Body)
				}
			}
		})
	}
}
