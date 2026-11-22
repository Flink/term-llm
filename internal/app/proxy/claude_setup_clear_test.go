package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClaudeSetupClearAllPreservesOtherAccessAndHistory(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	claudeSetupAccount(t, s, "claude-bin", "chatgpt") // Reserved-looking name is not ownership.
	other := Route{Name: "claude", Account: "claude-bin", LocalPrefix: "/claude/", Upstream: "https://chatgpt.com/backend-api/codex", Enabled: true}
	configure(t, s, Config{Routes: []Route{other}}, nil)
	first := claudeSetupPost(t, s, cookie, csrf)
	native := s.snap.routes[1]
	claudeSetupAccount(t, s, "manual-slot", "claude-bin")
	claudeSetupAccount(t, s, "unused-slot", "claude-bin")
	manual := claudeSetupRoute("manual-route", "manual-slot")
	manual.Enabled = false
	c := s.snap.config
	c.Routes = append(append([]Route{}, c.Routes...), manual)
	c.Accounts = map[string]AccountRef{
		"manual-slot":  {Kind: "claude-bin", File: "synthetic-not-read"},
		"missing-slot": {Kind: "claude-bin", File: "synthetic-not-read"},
		"claude-bin":   {Kind: "chatgpt", File: "synthetic-retained"},
	}
	configure(t, s, c, nil)
	mixed, mixedToken, err := s.store.createCaller("Mixed client", []string{native.Name, other.Name}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	manualClient, _, err := s.store.createCaller("Manual client", []string{manual.Name}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	empty, _, err := s.store.createCaller("Claude Code", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	otherClient, otherToken, err := s.store.createCaller("Other only", []string{other.Name}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	oldRevoked, _, err := s.store.createCaller("Already revoked", []string{native.Name}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.revokeCaller(context.Background(), oldRevoked.ID); err != nil {
		t.Fatal(err)
	}
	orphan, _, err := s.store.createCaller("Orphaned native", nil, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	// A stale enrollment on an existing Claude account still identifies the
	// affected caller, even if its recorded route no longer exists.
	execProxySQL(t, s.store, "INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires,reserved) VALUES(?,?,'gone-route','stale-native-authority',0,1)", "unused-slot", orphan.ID)
	execProxySQL(t, s.store, "INSERT INTO grants(caller,route,authority) VALUES(?, 'gone-route', 'stale-native-authority')", orphan.ID)
	// These are real synthetic ledger/debug records, not files belonging to a
	// user's config or login. Clearing setup must not prune any of them.
	rec, req := debugRequest("px_claude_clear")
	rec.CallerID, rec.Route = first.CallerID, native.Name
	if err := s.store.start(context.Background(), &rec); err != nil {
		t.Fatal(err)
	}
	if err := s.store.finish(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	capture := s.payloadDebug.begin(rec, req)
	if capture == nil {
		t.Fatal("missing synthetic capture")
	}
	capture.observe("request.body", []byte("synthetic body"), true, false, false)
	capture.finish(rec)
	capturePath := filepath.Join(capture.dir, "manifest.json")
	captureBefore, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	preserved := claudeSetupDBState(t, s, "requests", "money_history", "money_settings", "secrets")
	otherAccount, err := s.store.account("claude-bin")
	if err != nil {
		t.Fatal(err)
	}
	ctxNative, cancelNative := context.WithCancelCause(context.Background())
	ctxOther, cancelOther := context.WithCancelCause(context.Background())
	defer cancelNative(nil)
	defer cancelOther(nil)
	s.sockets.Store("native", &activeSocket{caller: mixed.ID, route: native.Name, account: native.Account, authority: native.authority, cancel: cancelNative})
	s.sockets.Store("other", &activeSocket{caller: mixed.ID, route: other.Name, account: other.Account, authority: s.snap.routes[0].authority, cancel: cancelOther})
	defer s.sockets.Delete("native")
	defer s.sockets.Delete("other")
	for i := 0; i < 2; i++ {
		revision := s.snap.revision
		out := adminRequest(s, "DELETE", "claude/setup", map[string]any{"revision": revision}, cookie, csrf)
		if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" || s.snap.revision != revision+1 {
			t.Fatalf("clear %d: %d %s", i, out.Code, out.Body.String())
		}
		if len(s.snap.routes) != 1 || s.snap.routes[0].Name != other.Name || !reflect.DeepEqual(s.snap.config.Accounts, map[string]AccountRef{"claude-bin": c.Accounts["claude-bin"]}) {
			t.Fatalf("clear scope incorrect: %+v", s.snap.config)
		}
		infos, err := s.store.accountInfos()
		if err != nil || len(infos) != 1 || infos[0].Kind != "chatgpt" {
			t.Fatalf("Claude slots retained or other slot removed: %+v %v", infos, err)
		}
		var rows int
		if err := s.store.db.QueryRow("SELECT count(*) FROM claude_shared_enrollment").Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("enrollments retained: %d %v", rows, err)
		}
		clients, err := s.store.callers()
		if err != nil || len(clients) != 7 {
			t.Fatalf("audit rows deleted: %+v %v", clients, err)
		}
		wantRevoked := map[string]bool{first.CallerID: true, manualClient.ID: true, oldRevoked.ID: true, orphan.ID: true}
		for _, client := range clients {
			if client.Revoked != wantRevoked[client.ID] {
				t.Fatalf("incorrect revocation: %+v (unrelated empty=%s)", client, empty.ID)
			}
			wantGrants := []string{}
			if client.ID == mixed.ID || client.ID == otherClient.ID {
				wantGrants = []string{other.Name}
			}
			if !reflect.DeepEqual(client.Grants, wantGrants) {
				t.Fatalf("wrong remaining grants: %+v", client)
			}
		}
		for _, token := range []string{mixedToken, otherToken} {
			if _, err := s.store.authorize(token, s.snap.routes[0]); err != nil {
				t.Fatalf("non-Claude token lost access: %v", err)
			}
		}
		if _, err := s.store.authorize(first.Token, native); err == nil {
			t.Fatal("Claude-only caller not revoked")
		}
		if after := claudeSetupDBState(t, s, "requests", "money_history", "money_settings", "secrets"); after != preserved {
			t.Fatal("history, money or secrets deleted")
		}
		if after, err := os.ReadFile(capturePath); err != nil || !reflect.DeepEqual(after, captureBefore) {
			t.Fatal("debug capture modified", err)
		}
		if after, err := s.store.account("claude-bin"); err != nil || !reflect.DeepEqual(after, otherAccount) {
			t.Fatal("non-Claude reserved name removed", err)
		}
	}
	if !errors.Is(context.Cause(ctxNative), errSocketRevoked) || context.Cause(ctxOther) != nil {
		t.Fatal("postcommit socket revocation lost mixed caller's other connection")
	}
}

func TestClaudeSetupClearFencesLateOAuthAfterIdenticalRecreation(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "code", true: "refresh"}[refresh], func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			out := claudeSetupPost(t, s, cookie, csrf)
			native := s.snap.routes[0]
			a, err := s.store.account(native.Account)
			if err != nil {
				t.Fatal(err)
			}
			other := Route{Name: "other", LocalPrefix: "/other/", Upstream: "https://example.test", Enabled: true}
			configure(t, s, Config{Routes: []Route{native.Route, other}}, nil)
			if err := s.store.updateGrants(out.CallerID, []string{native.Name, other.Name}, s.snap); err != nil {
				t.Fatal(err)
			}
			scope := claudeForwardScope{caller: out.CallerID, route: native.Name, account: native.Account, authority: native.authority, identity: a.identityVersion}
			ctx := context.Background()
			before := a
			if refresh {
				markNativeSetupAccountReady(t, s, a.Name, [32]byte{42})
				a, err = s.store.account(a.Name)
				if err != nil {
					t.Fatal(err)
				}
				before = a
				before.State = "refreshing"
				if err := s.store.changeClaudeSharedAccount(ctx, a, before); err != nil {
					t.Fatal(err)
				}
			} else if err := s.store.reserveClaudeSharedEnrollment(ctx, scope); err != nil {
				t.Fatal(err)
			}

			clear := adminRequest(s, "DELETE", "claude/setup", map[string]any{"revision": s.snap.revision}, cookie, csrf)
			if clear.Code != 200 {
				t.Fatal(clear.Code, clear.Body.String())
			}
			// Recreate the identical identity/grant. The mixed caller was correctly
			// retained; reservation deletion must independently fence completion.
			recreated := newNativeAccount(a.Name, a.Kind)
			recreated.Generation, recreated.identityVersion = before.Generation, before.identityVersion
			if refresh {
				recreated.State = "refreshing"
			}
			if err := s.store.replaceAccount(ctx, recreated, 0); err != nil {
				t.Fatal(err)
			}
			configure(t, s, Config{Routes: []Route{native.Route, other}}, nil)
			if err := s.store.updateGrants(out.CallerID, []string{native.Name, other.Name}, s.snap); err != nil {
				t.Fatal(err)
			}
			if err := claudeVaultBindingValid(context.Background(), s.store.db, scope); err != nil {
				t.Fatal("fixture must restore the original binding", err)
			}
			next := before
			next.Generation++
			if err := sealNativeAccount([32]byte{42}, &next, claudeVaultReply{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresIn: 3600}, ""); err != nil {
				t.Fatal(err)
			}
			if refresh {
				err = s.store.changeClaudeSharedAccount(ctx, before, next)
			} else {
				s.mu.Lock()
				err = s.finishClaudeSharedEnrollmentLocked(ctx, scope, before, next)
				s.mu.Unlock()
			}
			if !errors.Is(err, errClaudeVaultLogin) {
				t.Fatalf("late OAuth completion recreated custody: %v", err)
			}
			if got, err := s.store.account(a.Name); err != nil || len(got.claudeSharedCipher) != 0 || got.claudeSharedID != recreated.claudeSharedID {
				t.Fatal("late OAuth completion modified the new account", err)
			}
			var rows int
			if err := s.store.db.QueryRow("SELECT count(*) FROM claude_shared_enrollment").Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("late custody rows=%d err=%v", rows, err)
			}
		})
	}
}
