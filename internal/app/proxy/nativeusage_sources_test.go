package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type nativeUsageTestTransport func(*http.Request) (*http.Response, error)

func (f nativeUsageTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nativeUsageFixture(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func usageTestAccount(t *testing.T, s *Service, kind, name, access string) accountRecord {
	t.Helper()
	a := accountRecord{AccountInfo: AccountInfo{Name: name, Kind: kind, State: "ready", Generation: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()},
		credentials: accountCredentials{AccessToken: access, RefreshToken: "refresh-" + access, AccountID: "identity-" + name}}
	if p := nativeProviderForKind(kind); p != nil {
		a = newNativeAccount(name, kind)
		f, err := s.nativeForwarder(p)
		if err != nil {
			t.Fatal(err)
		}
		err = sealNativeAccount(f.ca.vaultKey, &a, claudeVaultReply{AccessToken: access, RefreshToken: "refresh-" + access,
			ExpiresIn: 3600, Scope: "user:profile user:inference"}, "fixture-secret")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.replaceAccount(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	return a
}

func usageTestRoute(id, root, account, key string) Route {
	r := Route{Name: id, LocalPrefix: "/" + id + "/", Enabled: true, Upstream: root, Account: account}
	if key != "" {
		r.SetHeaders = map[string]HeaderRule{"Authorization": {Secret: key, Prefix: "Bearer "}}
	}
	return r
}

func TestNativeUsageSourcesBoundFixtures(t *testing.T) {
	s := testService(t)
	usageTestAccount(t, s, "claude-bin", "claude-slot", "claude-bound")
	usageTestAccount(t, s, "agy-bin", "agy-slot", "agy-bound")
	usageTestAccount(t, s, "chatgpt", "chat-slot", "chat-bound")
	usageTestAccount(t, s, "grok", "grok-slot", "grok-bound")
	usageTestAccount(t, s, "grok", "unbound-grok-slot", "not-a-route-credential")
	t.Setenv("OPENCODE_API_KEY", "unrelated-ambient")
	t.Setenv("CHATGPT_USAGE_BASE_URL", "https://unexpected.invalid")
	t.Setenv("HTTPS_PROXY", "http://unexpected.invalid")
	openCode, _ := routePresetByID("opencode-go")
	configure(t, s, Config{Routes: []Route{
		usageTestRoute("subscription-claude", "https://"+claudeAPIHost, "claude-slot", ""),
		usageTestRoute("subscription-agy", "https://"+agyAPIHost, "agy-slot", ""),
		usageTestRoute("subscription-chat", "https://chatgpt.com/backend-api/codex", "chat-slot", ""),
		usageTestRoute("subscription-grok", "https://cli-chat-proxy.grok.com/v1", "grok-slot", ""),
		usageTestRoute("router", "https://openrouter.ai/api/v1", "", "routing"),
		usageTestRoute("worker-only", "https://opencode.ai/zen/go/v1", "", ""),
		openCode.newRoute("go-plan", "go-plan", "opencode"),
	}}, map[string]material{"routing": {Value: "openrouter-bound"}, "opencode": {Value: "opencode-bound"}})

	type step struct {
		method, endpoint, access, payload string
		headers                           map[string]string
		response                          string
	}
	steps := []step{
		{"GET", "https://opencode.ai/zen/go/v1/usage", "opencode-bound", "", nil,
			`{"usage":{"rolling":{"percent":0,"resetsAt":"2030-01-01T01:00:00Z"},"weekly":{"percent":17,"resetsAt":"2030-01-07T01:00:00Z"},"monthly":{"percent":23,"resetsAt":"2030-02-01T01:00:00Z"}}}`},
		{"GET", "https://openrouter.ai/api/v1/key", "openrouter-bound", "", nil,
			`{"data":{"usage":2,"limit":10,"limit_remaining":8,"is_management_key":false}}`},
		{"POST", "https://" + agyAPIHost + "/v1internal:loadCodeAssist", "agy-bound", `{"metadata":{"ideType":"ANTIGRAVITY"}}`, map[string]string{"User-Agent": "antigravity/cli/1.5.13"},
			`{"cloudaicompanionProject":"fixture-project"}`},
		{"POST", "https://" + agyAPIHost + "/v1internal:retrieveUserQuotaSummary", "agy-bound", `{"project":"fixture-project"}`, map[string]string{"User-Agent": "antigravity/cli/1.5.13"},
			`{"groups":[{"displayName":"Gemini","description":"Gemini shared pool","buckets":[{"window":"5h","remainingFraction":0.75},{"window":"weekly","remainingFraction":1},{"window":"requests","remainingAmount":"20","disabled":true}]}]}`},
		{"GET", "https://chatgpt.com/backend-api/wham/usage", "chat-bound", "", map[string]string{"ChatGPT-Account-ID": "identity-chat-slot"},
			`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":4,"limit_window_seconds":18000}},"additional_rate_limits":[{"limit_name":"Code Review","rate_limit":{"secondary_window":{"used_percent":7,"limit_window_seconds":604800,"reset_at":1900000000}}}]}`},
		{"GET", "https://" + claudeAPIHost + "/api/oauth/usage", "claude-bound", "", map[string]string{"Anthropic-Beta": "oauth-2025-04-20"},
			`{"five_hour":{"utilization":12,"resets_at":"2030-01-01T00:00:00Z"},"seven_day":{"utilization":34}}`},
		{"GET", "https://cli-chat-proxy.grok.com/v1/billing?format=credits", "grok-bound", "", map[string]string{"X-Userid": "identity-grok-slot", "X-Xai-Token-Auth": "xai-grok-cli", "X-Grok-Client-Mode": "headless"},
			`{"config":{"creditUsagePercent":12,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2030-01-01T00:00:00Z","end":"2030-01-08T00:00:00Z"}}}`},
	}
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
		if len(steps) == 0 {
			t.Fatalf("unexpected request %s", r.URL)
		}
		step := steps[0]
		steps = steps[1:]
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body.Close()
		}
		if r.Method != step.method || r.URL.String() != step.endpoint || string(body) != step.payload || r.Header.Get("Authorization") != "Bearer "+step.access {
			t.Fatalf("unexpected request: %s %s body %q (authorization matched: %t)", r.Method, r.URL, body, r.Header.Get("Authorization") == "Bearer "+step.access)
		}
		if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("usage sent an unrelated credential header")
		}
		for key, value := range step.headers {
			if r.Header.Get(key) != value {
				t.Errorf("wrong %s", key)
			}
		}
		if strings.Contains(step.endpoint, "opencode.ai") && (r.Header.Get("X-Opencode-Client") == "" || r.Header.Get("X-Opencode-Session") == "") {
			t.Error("OpenCode attribution missing")
		}
		return nativeUsageFixture(http.StatusOK, step.response), nil
	}))

	sources := s.nativeUsageSources()
	var ids []string
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	wantIDs := []string{"go-plan", "router", "subscription-agy", "subscription-chat", "subscription-claude", "subscription-grok"}
	if !slices.Equal(ids, wantIDs) || len(steps) != 7 {
		t.Fatalf("sources = %v; discovery should not send HTTP", ids)
	}
	for i, source := range sources {
		report, err := source.Fetch(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", source.ID, err)
		}
		if want := []int{3, 1, 1, 2, 2, 1}[i]; len(report.Groups) != want {
			t.Errorf("%s: got %d quota groups, want %d", source.ID, len(report.Groups), want)
		}
		if source.ID == "subscription-agy" {
			group := report.Groups[0]
			if group.Detail != "Gemini shared pool" || len(group.Windows) != 3 || !group.Windows[2].Disabled || !group.Windows[2].PercentUnknown {
				t.Errorf("Antigravity unmeasured bucket was not preserved: %+v", group)
			}
		}
	}
	if len(steps) != 0 {
		t.Fatal("missing upstream requests")
	}
}

func TestNativeUsageStaleSourceAndFixedOrigin(t *testing.T) {
	s := testService(t)
	old := map[string]material{"key": {Value: "old-bound-key"}}
	routes := []Route{
		usageTestRoute("vendor", "https://opencode.ai/zen/go/v1", "", "key"),
		usageTestRoute("loopback", "https://127.0.0.1/zen/go/v1", "", "key"),
		usageTestRoute("vendor-port", "https://opencode.ai:8443/zen/go/v1", "", "key"),
		usageTestRoute("vendor-root", "https://opencode.ai/zen/v1", "", "key"),
		usageTestRoute("cursor", "https://api2.cursor.sh", "", "key"),
	}
	r := usageTestRoute("wrong-scheme", "https://openrouter.ai/api/v1", "", "")
	literal := "Basic should-not-be-repurposed"
	r.SetHeaders = map[string]HeaderRule{"Authorization": {Value: &literal}}
	routes = append(routes, r)
	configure(t, s, Config{Routes: routes}, old)
	sources := s.nativeUsageSources()
	if len(sources) != 1 || sources[0].ID != "vendor" || strings.Contains(sources[0].Key, old["key"].Value) {
		t.Fatal("eligible origin/credential filtering failed")
	}
	configure(t, s, Config{Routes: routes}, map[string]material{"key": {Value: "new-bound-key"}})
	current := s.nativeUsageSources()[0]
	if current.Key == sources[0].Key {
		t.Fatal("secret rotation did not invalidate source identity")
	}
	var calls int
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "opencode.ai" || r.Header.Get("Authorization") != "Bearer new-bound-key" {
			t.Fatal("unexpected egress or credential")
		}
		resp := nativeUsageFixture(http.StatusFound, "echo new-bound-key")
		resp.Header.Set("Location", "https://127.0.0.1/steal")
		return resp, nil
	}))
	if _, err := sources[0].Fetch(context.Background()); !errors.Is(err, errNativeUsageChanged) || calls != 0 {
		t.Fatalf("stale source error %v, calls %d", err, calls)
	}
	if _, err := current.Fetch(context.Background()); err == nil || strings.Contains(err.Error(), "new-bound-key") || calls != 1 {
		t.Fatalf("redirect/error was followed or reflected: %v, calls %d", err, calls)
	}
}

func TestNativeUsageRefreshesSharedVaultWithoutCaller(t *testing.T) {
	s := testService(t)
	a := usageTestAccount(t, s, "claude-bin", "shared-slot", "old-shared-bearer")
	a.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := s.store.replaceAccount(context.Background(), a, a.Generation); err != nil {
		t.Fatal(err)
	}
	configure(t, s, Config{Routes: []Route{usageTestRoute("shared-claude", "https://"+claudeAPIHost, a.Name, "")}}, nil)
	source := s.nativeUsageSources()[0]
	var calls []string
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		if r.URL.String() == claudeTokenURL {
			data, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.Method != "POST" || r.Header.Get("Authorization") != "" || !strings.Contains(string(data), `"refresh_token":"refresh-old-shared-bearer"`) {
				t.Fatal("wrong native OAuth refresh request")
			}
			return nativeUsageFixture(200, `{"access_token":"updated-shared-bearer","expires_in":3600,"token_type":"Bearer"}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer updated-shared-bearer" {
			t.Fatal("quota read did not use the refreshed token")
		}
		return nativeUsageFixture(200, `{"model_scoped":[{"display_name":"updated-shared-bearer","utilization":8}]}`), nil
	}))
	report, err := source.Fetch(context.Background())
	usageURL := "https://" + claudeAPIHost + "/api/oauth/usage"
	if err != nil || !slices.Equal(calls, []string{claudeTokenURL, usageURL}) || len(report.Groups) != 1 || report.Groups[0].Name != "[redacted]" {
		t.Fatalf("refresh calls %v, report %+v, error %v", calls, report, err)
	}
	if next := s.nativeUsageSources()[0]; next.Key != source.Key {
		t.Fatal("refresh changed the account identity")
	}
}

func TestNativeUsageAgyStopsAfterAccountReplacement(t *testing.T) {
	s := testService(t)
	a := usageTestAccount(t, s, "agy-bin", "shared-slot", "agy-access-before-replace")
	configure(t, s, Config{Routes: []Route{usageTestRoute("shared-agy", "https://"+agyAPIHost, a.Name, "")}}, nil)
	source := s.nativeUsageSources()[0]
	var calls int
	s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls != 1 || r.URL.Path != "/v1internal:loadCodeAssist" {
			t.Fatal("quota request used credentials from the replaced account")
		}
		next := a
		next.identityVersion++
		next.Generation++
		if err := sealNativeAccount(s.agyNative.ca.vaultKey, &next, claudeVaultReply{AccessToken: "new-agy-access", RefreshToken: "new-agy-refresh", ExpiresIn: 3600}, "fixture-secret"); err != nil {
			t.Fatal(err)
		}
		if err := s.store.replaceAccount(r.Context(), next, a.Generation); err != nil {
			t.Fatal(err)
		}
		return nativeUsageFixture(http.StatusOK, `{"cloudaicompanionProject":"stale-project"}`), nil
	}))
	if _, err := source.Fetch(context.Background()); !errors.Is(err, errNativeUsageChanged) || calls != 1 {
		t.Fatalf("account replacement did not stop quota fetch: %v, calls %d", err, calls)
	}
	if got := s.nativeUsageSources(); len(got) != 1 || got[0].Key == source.Key {
		t.Fatal("replacement did not invalidate source identity")
	}
}

func TestNativeUsageUnauthorizedPreservesInferenceToken(t *testing.T) {
	for _, tc := range []struct{ kind, root, path string }{
		{"chatgpt", "https://chatgpt.com/backend-api/codex", "/backend-api/wham/usage"},
		{"grok", "https://cli-chat-proxy.grok.com/v1", "/v1/billing"},
		{"claude-bin", "https://" + claudeAPIHost, "/api/oauth/usage"},
		{"agy-bin", "https://" + agyAPIHost, "/v1internal:loadCodeAssist"},
		{"agy-bin", "https://" + agyAPIHost, "/v1internal:retrieveUserQuotaSummary"},
	} {
		t.Run(tc.kind+tc.path, func(t *testing.T) {
			ctx := context.Background()
			s := testService(t)
			const token = "fixture-original-access"
			a := usageTestAccount(t, s, tc.kind, "slot", token)
			configure(t, s, Config{Routes: []Route{usageTestRoute("usage", tc.root, a.Name, "")}}, nil)
			before, err := s.store.account(a.Name)
			if err != nil {
				t.Fatal(err)
			}
			var rejected bool
			s.transport.RegisterProtocol("https", nativeUsageTestTransport(func(r *http.Request) (*http.Response, error) {
				if rejected || r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatalf("unexpected credential request: %s", r.URL.Path)
				}
				if tc.path == "/v1internal:retrieveUserQuotaSummary" && r.URL.Path == "/v1internal:loadCodeAssist" {
					return nativeUsageFixture(200, `{"cloudaicompanionProject":"fixture-project"}`), nil
				}
				if r.URL.Path != tc.path {
					t.Fatalf("unexpected endpoint %s", r.URL.Path)
				}
				rejected = true
				return nativeUsageFixture(http.StatusUnauthorized, ""), nil
			}))
			if _, err := s.nativeUsageSources()[0].Fetch(ctx); !errors.Is(err, nativeUsageHTTPStatus(http.StatusUnauthorized)) || !rejected {
				t.Fatalf("expected quota 401, got %v", err)
			}
			after, err := s.store.account(a.Name)
			if err != nil || !reflect.DeepEqual(before, after) || s.accountGenerationRejected(after) {
				t.Fatal("quota rejection changed inference account")
			}
			var access string
			if p := nativeProviderForKind(tc.kind); p != nil {
				f, e := s.nativeForwarder(p)
				if e != nil {
					t.Fatal(e)
				}
				_, access, err = s.boundNativeAccess(ctx, f.ca.vaultKey, a.Name, 0, func(record accountRecord) bool { return nativeUsageIdentity(record) == nativeUsageIdentity(before) }, func() bool { return true })
			} else {
				account, e := s.ensureAccount(ctx, a.Name, 0)
				access, err = account.credentials.AccessToken, e
			}
			if err != nil || access != token {
				t.Fatalf("original inference credential unavailable: %v", err)
			}
		})
	}
}
