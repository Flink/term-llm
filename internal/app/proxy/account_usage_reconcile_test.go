package proxy

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"
)

func TestAccountFailureRemovesUsageBinding(t *testing.T) {
	for _, tc := range []struct{ kind, root string }{
		{"chatgpt", "https://chatgpt.com/backend-api"},
		{"claude-bin", "https://" + claudeAPIHost},
		{"agy-bin", "https://" + agyAPIHost},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s := testService(t)
			a := usageTestAccount(t, s, tc.kind, "slot", "synthetic-access")
			configure(t, s, Config{Routes: []Route{usageTestRoute("subscription", tc.root, a.Name, "")}}, nil)
			s.mu.Lock()
			s.reconcileNativeUsageLocked()
			s.mu.Unlock()
			if !s.nativeUsage.HasSources() {
				t.Fatal("fixture missing usage source")
			}
			if p := nativeProviderForKind(tc.kind); p != nil {
				f, err := s.nativeForwarder(p)
				if err != nil {
					t.Fatal(err)
				}
				s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != p.tokenURL {
						t.Errorf("unexpected synthetic request: %s", r.URL.Path)
					}
					return nativeUsageFixture(400, ""), nil
				}))
				_, _, err = s.boundNativeAccess(t.Context(), f.ca.vaultKey, a.Name, a.Generation, func(accountRecord) bool { return true }, func() bool { return true })
				if err == nil {
					t.Fatal("rejected refresh succeeded")
				}
			} else {
				s.failAccount(t.Context(), a)
			}
			after, err := s.store.account(a.Name)
			if err != nil || after.State != "needs_login" {
				t.Fatalf("after failure: state %q, error %v", after.State, err)
			}
			if providers := s.nativeUsage.Snapshot().Providers; len(providers) != 0 {
				t.Fatalf("failed account retained %d usage bindings", len(providers))
			}
		})
	}
}

func TestSharedEnrollmentPublishesUsageBinding(t *testing.T) {
	f := newClaudeSharedHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { claudeSharedIssueCode(w, 3600, sharedFixtureScope) }, nil)
	s := f.service
	cookie, csrf := loginAdmin(t, s)
	operator := claudeSharedSetup(t, s, cookie, csrf, "create", "")
	if s.nativeUsage.HasSources() {
		t.Fatal("unconnected subscription is pollable")
	}
	metadata := f.discover(t, operator, http.StatusOK)
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, operator.Token), "subscription-code", true)
	if !s.nativeUsage.HasSources() {
		t.Fatal("newly enrolled subscription missing from usage")
	}
	seedAccountUsageReport(t, s)
	operator = claudeSharedSetup(t, s, cookie, csrf, "connect", metadata.Route)
	metadata = f.discover(t, operator, http.StatusOK)
	claudeSharedCode(t, claudeSharedNativeClient(t, metadata, operator.Token), "replacement-code", true)
	if providers := s.nativeUsage.Snapshot().Providers; len(providers) != 1 || providers[0].Report != nil {
		t.Fatal("shared re-enrollment did not replace cached usage")
	}
}

// Seed the real cache with a report for the current binding, but stop polling
// before reconnect so no new report can obscure whether the old one survived.
func seedAccountUsageReport(t *testing.T, s *Service) {
	t.Helper()
	sources := s.nativeUsageSources()
	if len(sources) != 1 {
		t.Fatalf("got %d usage sources, want one", len(sources))
	}
	sources[0].Fetch = func(context.Context) (nativeUsageReport, error) {
		return nativeUsageReport{Plan: "previous subscription"}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s.nativeUsage.Reconcile(sources)
	s.nativeUsage.Start(ctx)
	for ctx.Err() == nil {
		if p := s.nativeUsage.Snapshot().Providers; len(p) == 1 && p[0].Report != nil {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("fixture did not cache prior report")
}

func TestDeviceReconnectDropsSupersededUsage(t *testing.T) {
	s := testService(t)
	result := fakeChatGPTSetup(s)
	cookie, csrf := loginAdmin(t, s)
	finish := func(in deviceSetupInput) {
		path := startChatGPTSetupTest(t, s, cookie, csrf, in)
		result <- chatGPTSetupTestCredentials()
		s.deviceSetup.workers.Wait()
		deviceSetupStatusTest(t, s, cookie, path, deviceSetupStatus{Status: "connected", Route: "chatgpt"})
	}
	finish(deviceSetupInput{})
	seedAccountUsageReport(t, s)
	finish(deviceSetupInput{Account: "chatgpt"})
	if providers := s.nativeUsage.Snapshot().Providers; len(providers) != 1 || providers[0].Report != nil {
		t.Fatal("device reconnect did not replace cached usage")
	}
}
