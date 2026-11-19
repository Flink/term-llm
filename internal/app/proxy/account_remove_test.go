package proxy

import (
	"database/sql"
	"errors"
	"testing"
)

// Deleting a ChatGPT route leaves its account behind. Removing that account
// must free the name so the route (with its companions) can be recreated.
func TestRemoveUnusedAccountAllowsRecreatingChatGPTRoute(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	admin, csrf := loginAdmin(t, s)
	path := startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "connected", Route: "chatgpt"})

	remove := accountInput{Name: "chatgpt", Remove: true}
	if w := adminRequest(s, "DELETE", "accounts", remove, admin, csrf); w.Code != 409 {
		t.Fatalf("removed an account routes still use: %d %s", w.Code, w.Body)
	}
	if w := adminRequest(s, "PUT", "config", map[string]any{"revision": s.snap.revision, "routes": []Route{}}, admin, csrf); w.Code != 200 {
		t.Fatalf("delete routes: %d %s", w.Code, w.Body)
	}
	if w := adminRequest(s, "POST", chatGPTSetupPath, chatGPTSetupInput{}, admin, csrf); w.Code != 409 {
		t.Fatalf("leftover account should block reuse of its name: %d %s", w.Code, w.Body)
	}
	if w := adminRequest(s, "DELETE", "accounts", remove, admin, csrf); w.Code != 200 {
		t.Fatalf("remove unused account: %d %s", w.Code, w.Body)
	}
	if _, err := s.store.account("chatgpt"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("account still stored: %v", err)
	}
	if w := adminRequest(s, "DELETE", "accounts", remove, admin, csrf); w.Code != 404 {
		t.Fatalf("second removal: %d, want 404", w.Code)
	}

	path = startChatGPTSetupTest(t, s, admin, csrf, chatGPTSetupInput{})
	result <- chatGPTSetupTestCredentials()
	s.chatgptSetup.workers.Wait()
	chatGPTSetupStatusTest(t, s, admin, path, chatGPTSetupStatus{Status: "connected", Route: "chatgpt"})
	var names []string
	for _, route := range s.snap.config.Routes {
		names = append(names, route.Name)
	}
	if len(names) != 3 || names[0] != "chatgpt" || names[1] != "chatgpt-backend" || names[2] != "chatgpt-openai" {
		t.Fatalf("recreated routes = %v", names)
	}
}
