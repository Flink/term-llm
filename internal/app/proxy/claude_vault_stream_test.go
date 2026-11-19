package proxy

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClaudeSharedStreamsAndRevocationCancels(t *testing.T) {
	f := newClaudeForwardFixture(t)
	vendorDone := make(chan struct{})
	f.api = func(w http.ResponseWriter, r *http.Request) {
		defer close(vendorDone)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"model\":\"fixture\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	account, err := f.service.store.account("claude")
	if err != nil || account.State != "ready" || account.credentials.AccessToken != "" {
		t.Fatal("fixture is not a sealed shared account", err)
	}
	scope := claudeSharedTestCallerScope(t, f.service, f.service.snap.routes[0], f.token)
	conn, reader := f.native(t)
	body := `{"model":"fixture"}`
	if _, err := fmt.Fprintf(conn, "POST /v1/messages HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", claudeAPIHost, f.access, len(body), body); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("shared account failed SSE admission", response.StatusCode)
	}
	first := make([]byte, len("event: message_start\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "event: message_start\n" {
		t.Fatal("native stream did not start", err)
	}
	f.service.mu.Lock()
	err = f.service.store.updateGrants(scope.caller, nil, f.service.snap)
	f.service.cancelCallerSockets(scope.caller)
	f.service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-vendorDone:
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not cancel native upstream SSE")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	claudeForwardWaitSockets(t, f.service)
	records, _, err := f.service.store.records(nil, 100)
	if err != nil || len(records) != 1 || records[0].Outcome != errSocketRevoked.Error() {
		t.Fatal("native revoked stream ledger not finalized", records, err)
	}
	if f.vendorCalls.Load() != 1 {
		t.Fatal("native stream was replayed")
	}
}

func TestClaudeVaultReplyPreservesOnlyGrantedSubscriptionScopes(t *testing.T) {
	const scopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
	reply, err := parseClaudeVaultReply([]byte(`{"access_token":"real-access","refresh_token":"real-refresh","token_type":"Bearer","expires_in":3600,"scope":"` + scopes + ` secret-echo user:profile"}`))
	if err != nil || reply.Scope != scopes {
		t.Fatal("known native capability lost or unknown metadata leaked", reply.Scope, err)
	}
	minimal, err := parseClaudeVaultReply([]byte(`{"access_token":"real-access","refresh_token":"real-refresh","token_type":"Bearer","expires_in":3600,"scope":"user:inference user:profile"}`))
	if err != nil || minimal.Scope != "user:inference user:profile" {
		t.Fatal("ungranted native scope manufactured", minimal.Scope, err)
	}
}
