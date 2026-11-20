package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const manageTestURL = "http://localhost/_proxy/manage/v1/"

type issuedMachineClient struct {
	Client Caller `json:"client"`
	Token  string `json:"token"`
}

func machineRequest(method, path, token string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, manageTestURL+path, body)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func serveManage(s *Service, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func manageRequest(s *Service, method, path, token string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	return serveManage(s, machineRequest(method, path, token, bytes.NewReader(data)))
}

func createMachineClient(t *testing.T, s *Service, token, name string, grants []string, external string) issuedMachineClient {
	t.Helper()
	if grants == nil {
		grants = []string{}
	}
	body := map[string]any{"name": name, "grants": grants, "revision": s.snap.revision}
	if external != "" {
		body["external_id"] = external
	}
	return proxyResponse[issuedMachineClient](t, manageRequest(s, "POST", "clients", token, body), 201)
}

func seedManageEnrollment(t *testing.T, s *Service, id, route string) {
	t.Helper()
	execProxySQL(t, s.store, `INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires) VALUES(?,?,?,'fixture-authority',0)`, id+"/"+route, id, route)
}

func fakeManageSocket(t *testing.T, s *Service, id string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	key := new(int)
	s.sockets.Store(key, &activeSocket{caller: id, cancel: cancel})
	t.Cleanup(func() { s.sockets.Delete(key); cancel(nil) })
	return ctx
}

func TestManageAPIBoundary(t *testing.T) {
	s := testService(t)
	configure(t, s, Config{Routes: []Route{{Name: "demo", LocalPrefix: "/demo/", Upstream: "https://example.test", Enabled: true}}}, nil)
	cookie, csrf := loginAdmin(t, s)
	issued := issueAdminKey(t, s, cookie, csrf, "Automation")
	clientToken := caller(t, s, "demo")
	for _, tt := range []struct {
		name, method, path, token string
		alter                     func(*http.Request)
		code                      int
	}{
		{"valid", "GET", "routes", issued.Token, nil, 200},
		{"valid with cookie", "GET", "routes", issued.Token, func(r *http.Request) { r.AddCookie(cookie) }, 200},
		{"cookie alone", "GET", "routes", "", func(r *http.Request) { r.AddCookie(cookie) }, 401},
		{"no credentials", "GET", "routes", "", nil, 401},
		{"caller token", "GET", "routes", clientToken, nil, 401},
		{"browser session bearer", "GET", "routes", cookie.Value, nil, 401},
		{"host admin token", "GET", "routes", strings.Repeat("a", 64), nil, 401},
		{"duplicate", "GET", "routes", issued.Token, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+issued.Token) }, 401},
		{"comma combined", "GET", "routes", issued.Token, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+issued.Token+", Bearer "+issued.Token) }, 401},
		{"origin", "GET", "routes", issued.Token, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") }, 403},
		{"empty origin", "GET", "routes", issued.Token, func(r *http.Request) { r.Header.Set("Origin", "") }, 403},
		{"null origin", "GET", "routes", issued.Token, func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"options", "OPTIONS", "routes", "", nil, 405},
		{"unknown Claude suffix", "GET", "unknown/" + claudeForwardDiscovery, "", nil, 404},
		{"unknown path", "GET", "secret", issued.Token, nil, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := machineRequest(tt.method, tt.path, tt.token, nil)
			if tt.alter != nil {
				tt.alter(r)
			}
			w := serveManage(s, r)
			proxyResponse[map[string]any](t, w, tt.code)
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("unsafe management response headers")
			}
		})
	}
	for _, path := range []string{"/demo/work", "/_proxy/api/state", "/_proxy/client/v1/whoami"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "localhost"
		r.Header.Set("Authorization", "Bearer "+issued.Token)
		if w := serveManage(s, r); w.Code != 401 {
			t.Fatalf("admin API key escaped management: %s → %d", path, w.Code)
		}
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM admin_keys WHERE last_used>0") != 1 {
		t.Fatal("last_used not recorded")
	}
	execProxySQL(t, s.store, `CREATE TRIGGER repeat_touch BEFORE UPDATE OF last_used ON admin_keys BEGIN SELECT RAISE(ABORT,'touched twice'); END`)
	proxyResponse[map[string]any](t, manageRequest(s, "GET", "routes", issued.Token, nil), 200)
}

func TestManageAPIThrottleAdmissionAndDBErrors(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	unknown := randomToken(adminKeyPrefix)
	// A saturated slot cannot reach SQLite (even if the key table is unavailable).
	s.slots = make(chan struct{}, 1)
	s.slots <- struct{}{}
	execProxySQL(t, s.store, "ALTER TABLE admin_keys RENAME TO unavailable_keys")
	response := proxyResponse[map[string]string](t, manageRequest(s, "GET", "routes", key.Token, nil), 503)
	if response["error"] != "Proxy concurrency limit" {
		t.Fatal(response)
	}
	<-s.slots
	for range 9 {
		proxyResponse[map[string]string](t, manageRequest(s, "GET", "routes", unknown, nil), 503)
	}
	execProxySQL(t, s.store, "ALTER TABLE unavailable_keys RENAME TO admin_keys")
	for i := range 9 {
		r := machineRequest("GET", "routes", unknown, nil)
		r.RemoteAddr = "192.0.2.1:" + string(rune('1'+i))
		w := serveManage(s, r)
		if i < 8 {
			proxyResponse[map[string]string](t, w, 401)
			continue
		}
		proxyResponse[map[string]string](t, w, 429)
		if w.Header().Get("Retry-After") != "60" {
			t.Fatal("missing retry interval")
		}
	}
	r := machineRequest("GET", "routes", key.Token, nil)
	r.RemoteAddr = "192.0.2.1:9999"
	proxyResponse[map[string]any](t, serveManage(s, r), 200)
	proxyResponse[map[string]string](t, adminRequest(s, "POST", "login", map[string]string{"token": "bad"}, nil, ""), 401)
	now := time.Now()
	for i := range managePeerLimit + 10 {
		s.manageGate.limited(string(rune(i)), now, true)
	}
	if len(s.manageGate.peers) != managePeerLimit {
		t.Fatal("unbounded peer buckets")
	}
	proxyResponse[map[string]any](t, manageRequest(s, "GET", "routes", key.Token, nil), 200)
	if s.manageGate.limited("new-peer", now.Add(time.Minute), true) {
		t.Fatal("expired buckets blocked peer")
	}
}

type manageDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
	onWrite  func(time.Time)
}

func (w *manageDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *manageDeadlineRecorder) Write(p []byte) (int, error) {
	w.onWrite(w.deadline)
	return w.ResponseRecorder.Write(p)
}

func TestManageAPIHoldsSlotThroughBoundedResponseWrite(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	for _, tt := range []struct {
		name, token string
		status      int
	}{
		{"success", key.Token, http.StatusOK},
		{"auth error", "", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var wrote bool
			start := time.Now()
			w := &manageDeadlineRecorder{ResponseRecorder: httptest.NewRecorder(), onWrite: func(deadline time.Time) {
				wrote = true
				if !deadline.After(time.Now()) || deadline.After(start.Add(time.Minute)) {
					t.Errorf("expected a bounded response write deadline, got %v", deadline)
				}
				if len(s.slots) != 1 {
					t.Error("admission slot was not held during write")
				}
			}}
			s.ServeHTTP(w, machineRequest("GET", "routes", tt.token, nil))
			if !wrote || w.Code != tt.status || !w.deadline.IsZero() || len(s.slots) != 0 {
				t.Fatalf("response or cleanup failed: status=%d wrote=%t deadline=%v slots=%d", w.Code, wrote, w.deadline, len(s.slots))
			}
		})
	}
}

func TestManageAPIProjectsOnlyAllowlistedRouteFields(t *testing.T) {
	s := testService(t)
	literal := "TOP-SECRET-HEADER"
	routes := []Route{
		{Name: "plain", LocalPrefix: "/plain/", WebSocket: true, Enabled: true, Upstream: "https://hidden.example.test/path", SetHeaders: map[string]HeaderRule{"x-secret": {Value: &literal}}},
		{Name: "rules", LocalPrefix: "/rules/", Rules: []RouteRule{{PathPrefix: "/messages/", Upstream: "https://hidden.example.test", CallerAuth: Auth{Header: "X-Api-Key"}}}},
	}
	configure(t, s, Config{Routes: routes}, nil)
	s.snap.routes[0].Account = "PRIVATE-ACCOUNT"
	execProxySQL(t, s.store, "INSERT INTO accounts VALUES('PRIVATE-ACCOUNT','fixture','ready',0,0,0,'PRIVATE-ACCOUNT-DATA')")
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	got := proxyResponse[map[string]any](t, manageRequest(s, "GET", "routes", key.Token, nil), 200)
	var want map[string]any
	_ = json.Unmarshal([]byte(`{"routes":[{"name":"plain","provider":"plain","profile":"","local_prefix":"/plain/","enabled":true,"websocket":true,"account_state":"ready","caller_auth":{"header":"Authorization","prefix":"Bearer "},"rules":[]},{"name":"rules","provider":"rules","profile":"","local_prefix":"/rules/","enabled":false,"websocket":false,"caller_auth":{"header":"","prefix":""},"rules":[{"path_prefix":"/messages/","caller_auth":{"header":"X-Api-Key","prefix":""}}]}]}`), &want)
	want["revision"] = float64(s.snap.revision)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected projection: %+v", got)
	}
}

func TestManageAPIReusesAccountStateAcrossRoutes(t *testing.T) {
	s := testService(t)
	for i, account := range []string{"shared", "shared", "other", "missing", ""} {
		name := "route-" + string(rune('a'+i))
		s.snap.routes = append(s.snap.routes, activeRoute{Route: Route{Name: name, Account: account}})
	}
	execProxySQL(t, s.store, `INSERT INTO accounts VALUES
		('shared','fixture','ready',0,0,0,'private'),
		('other','fixture','needs_login',0,0,0,'private'),
		('unreferenced','fixture','ready',0,0,0,'private');
		ALTER TABLE accounts RENAME TO account_fixture;
		CREATE VIEW accounts AS SELECT name, CASE name
			WHEN 'shared' THEN hex(randomblob(16))
			WHEN 'unreferenced' THEN abs(-9223372036854775808)
			ELSE state END AS state FROM account_fixture;`)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	got := proxyResponse[struct{ Routes []manageRouteInfo }](t, manageRequest(s, "GET", "routes", key.Token, nil), 200)
	if len(got.Routes) != 5 {
		t.Fatalf("wanted five routes, got %+v", got.Routes)
	}
	// The shared account's synthetic state changes on each read. Both routes
	// must use the same cached value, without evaluating unreferenced rows.
	shared := got.Routes[0].AccountState
	if len(shared) != 32 || got.Routes[1].AccountState != shared {
		t.Fatal("shared account state was not read once and reused")
	}
	if got.Routes[2].AccountState != "needs_login" || got.Routes[3].AccountState != "unavailable" || got.Routes[4].AccountState != "" {
		t.Fatalf("wrong states for distinct, missing, or absent accounts: %+v", got.Routes)
	}
}

func TestManageAPIRequiresExplicitGrants(t *testing.T) {
	s := testService(t)
	configure(t, s, Config{Routes: []Route{{Name: "demo", LocalPrefix: "/demo/", Upstream: "https://example.test", Enabled: true}}}, nil)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	c := createMachineClient(t, s, key.Token, "Existing client", []string{"demo"}, "").Client
	seedManageEnrollment(t, s, c.ID, "demo")
	ws := fakeManageSocket(t, s, c.ID)
	path := "clients/" + c.ID
	for _, variant := range []string{"missing", "null"} {
		t.Run(variant, func(t *testing.T) {
			create := map[string]any{"name": "New client", "revision": s.snap.revision}
			regrant := map[string]any{"revision": s.snap.revision}
			if variant == "null" {
				create["grants"] = nil
				regrant["grants"] = nil
			}
			proxyResponse[map[string]string](t, manageRequest(s, "POST", "clients", key.Token, create), 400)
			proxyResponse[map[string]string](t, manageRequest(s, "PUT", path+"/grants", key.Token, regrant), 400)
			got := proxyResponse[struct{ Client Caller }](t, manageRequest(s, "GET", path, key.Token, nil), 200)
			if !reflect.DeepEqual(got.Client.Grants, c.Grants) || proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE caller=? AND route='demo'", c.ID) != 1 || ws.Err() != nil {
				t.Fatal("rejected regrant changed grants, pending enrollment, or socket state")
			}
			if proxyCount(t, s.store, "SELECT count(*) FROM callers") != 1 {
				t.Fatal("rejected create persisted a caller")
			}
		})
	}
	empty := createMachineClient(t, s, key.Token, "Empty grants", []string{}, "")
	if empty.Client.Grants == nil || len(empty.Client.Grants) != 0 {
		t.Fatal("create with explicit empty grants did not return an empty array")
	}
	got := proxyResponse[struct{ Client Caller }](t, manageRequest(s, "PUT", path+"/grants", key.Token, map[string]any{"revision": s.snap.revision, "grants": []string{}}), 200)
	if got.Client.Grants == nil || len(got.Client.Grants) != 0 || proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment WHERE caller=?", c.ID) != 0 || !errors.Is(context.Cause(ws), errSocketRevoked) {
		t.Fatal("explicit empty regrant did not clear grants, pending enrollment, and sockets")
	}
}

func TestManageAPIClientLifecycle(t *testing.T) {
	s := testService(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer upstream.Close()
	configure(t, s, Config{Routes: []Route{
		{Name: "first", LocalPrefix: "/first/", Enabled: true, AllowHTTP: true, Upstream: upstream.URL},
		{Name: "second", LocalPrefix: "/second/", Enabled: true, AllowHTTP: true, Upstream: upstream.URL},
	}}, nil)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	rot := issueAdminKey(t, s, cookie, csrf, "Rotated key")
	manual, _, err := s.store.createCaller("Manual", []string{"first"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	c := createMachineClient(t, s, key.Token, " Provisioned ", []string{"first"}, "machine:a/1")
	if c.Client.Name != "Provisioned" || c.Client.CreatedBy != key.Key.ID || c.Client.ExternalID != "machine:a/1" || !strings.HasPrefix(c.Token, "sk-tlp-") {
		t.Fatal("invalid create response")
	}
	target := "clients/" + c.Client.ID
	rev := s.snap.revision
	list := proxyResponse[struct{ Clients []Caller }](t, manageRequest(s, "GET", "clients?external_id=machine:a/1", rot.Token, nil), 200)
	if len(list.Clients) != 1 || list.Clients[0].ID != c.Client.ID {
		t.Fatalf("unexpected lookup: %+v", list)
	}
	list = proxyResponse[struct{ Clients []Caller }](t, manageRequest(s, "GET", "clients", rot.Token, nil), 200)
	if len(list.Clients) != 1 || list.Clients[0].ID != c.Client.ID {
		t.Fatal("browser client visible")
	}
	assertForward := func(path string, code int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/"+path+"/work", nil)
		r.Header.Set("Authorization", "Bearer "+c.Token)
		if w := serveManage(s, r); w.Code != code {
			t.Fatalf("forward %s: %d", path, w.Code)
		}
	}
	assertForward("first", 204)
	assertForward("second", 401)
	conflict := proxyResponse[map[string]string](t, manageRequest(s, "POST", "clients", rot.Token, map[string]any{"name": "Duplicate", "grants": []string{}, "revision": rev, "external_id": "machine:a/1"}), 409)
	if conflict["client_id"] != c.Client.ID {
		t.Fatal("conflict did not identify active client")
	}
	for _, tt := range []struct {
		method, path string
		body         any
		code         int
	}{
		{"GET", "clients/" + manual.ID, nil, 404}, {"GET", "clients/missing", nil, 404},
		{"DELETE", "clients/" + manual.ID, nil, 404}, {"DELETE", "clients/" + manual.ID + "?purge=1", nil, 404},
		{"PUT", "clients/" + manual.ID + "/grants", map[string]any{"grants": []string{}, "revision": rev}, 404},
		{"POST", "clients", map[string]any{"name": "Missing revision", "grants": []string{}}, 400},
		{"POST", "clients", map[string]any{"name": "Stale revision", "grants": []string{}, "revision": rev - 1}, 409},
		{"POST", "clients", map[string]any{"name": "Invalid external id", "grants": []string{}, "revision": rev, "external_id": "bad id"}, 400},
		{"POST", "clients", map[string]any{"name": "Invalid route", "revision": rev, "grants": []string{"unknown"}}, 400},
		{"PUT", target + "/grants", map[string]any{"grants": []string{"second"}}, 400},
		{"PUT", target + "/grants", map[string]any{"grants": []string{}, "revision": rev - 1}, 409},
		{"PUT", target + "/grants", map[string]any{"revision": rev, "grants": []string{"second", "missing"}}, 400},
		{"PUT", target + "/grants", map[string]any{"revision": rev, "grants": []string{"second", "second"}}, 400},
		{"DELETE", target + "?purge=1", nil, 409},
	} {
		proxyResponse[map[string]string](t, manageRequest(s, tt.method, tt.path, rot.Token, tt.body), tt.code)
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM callers") != 2 {
		t.Fatal("rejected create changed state")
	}
	assertForward("first", 204)
	seedManageEnrollment(t, s, c.Client.ID, "first")
	ws := fakeManageSocket(t, s, c.Client.ID)
	g := proxyResponse[struct{ Client Caller }](t, manageRequest(s, "PUT", target+"/grants", rot.Token, map[string]any{"revision": rev, "grants": []string{"second"}}), 200)
	if !reflect.DeepEqual(g.Client.Grants, []string{"second"}) || !errors.Is(context.Cause(ws), errSocketRevoked) || proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment") != 0 {
		t.Fatal("regrant did not replace grants, clear enrollment, or cancel socket")
	}
	assertForward("first", 401)
	assertForward("second", 204)
	ws = fakeManageSocket(t, s, c.Client.ID)
	seedManageEnrollment(t, s, c.Client.ID, "second")
	proxyResponse[map[string]bool](t, manageRequest(s, "DELETE", target, rot.Token, nil), 200)
	if !errors.Is(context.Cause(ws), errSocketRevoked) || proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment") != 0 {
		t.Fatal("revoke did not clear enrollment or cancel socket")
	}
	assertForward("second", 401)
	proxyResponse[map[string]string](t, manageRequest(s, "DELETE", target, rot.Token, nil), 409)
	proxyResponse[map[string]string](t, manageRequest(s, "PUT", target+"/grants", rot.Token, map[string]any{"grants": []string{}, "revision": rev}), 409)
	replacement := createMachineClient(t, s, rot.Token, "Replacement", nil, "machine:a/1")
	if replacement.Client.ID == c.Client.ID {
		t.Fatal("external ID not reused by new client")
	}
	seedManageEnrollment(t, s, c.Client.ID, "stray")
	proxyResponse[map[string]bool](t, manageRequest(s, "DELETE", target+"?purge=1", rot.Token, nil), 200)
	proxyResponse[map[string]string](t, manageRequest(s, "GET", target, rot.Token, nil), 404)
	if proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment") != 0 || proxyCount(t, s.store, "SELECT count(*) FROM grants WHERE caller=?", c.Client.ID) != 0 {
		t.Fatal("purge left dependent rows")
	}
}

type observedManageBody struct {
	io.Reader
	entered chan struct{}
}

func (b *observedManageBody) Read(p []byte) (int, error) {
	if b.entered != nil {
		close(b.entered)
		b.entered = nil
	}
	return b.Reader.Read(p)
}

func TestManageCreateRevalidatesAfterConcurrentCascade(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Revoked during create")
	existing := createMachineClient(t, s, key.Token, "Already committed", nil, "")
	data, _ := json.Marshal(map[string]any{"name": "Queued", "grants": []string{}, "revision": s.snap.revision})
	rd, wr := io.Pipe()
	defer rd.Close()
	defer wr.Close()
	entered := make(chan struct{})
	r := machineRequest("POST", "clients", key.Token, &observedManageBody{Reader: rd, entered: entered})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- serveManage(s, r) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not reach body after its first key lookup")
	}
	revoke := map[string]any{"id": key.Key.ID, "revoke_clients": true}
	proxyResponse[map[string]any](t, adminRequest(s, "DELETE", "admin-keys", revoke, cookie, csrf), 200)
	if _, err := wr.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = wr.Close()
	select {
	case w := <-result:
		proxyResponse[map[string]string](t, w, 401)
	case <-time.After(5 * time.Second):
		t.Fatal("queued create did not finish")
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE created_by=? AND revoked=0", key.Key.ID) != 0 || proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE id=? AND revoked=1", existing.Client.ID) != 1 {
		t.Fatal("deleted key left active callers")
	}
}

func TestManagePurgePreservesCompletedAndInFlightHistory(t *testing.T) {
	s := testService(t)
	started := make(chan struct{})
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(started)
			<-done
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	closed := false
	defer func() {
		if !closed {
			close(done)
		}
	}()
	configure(t, s, Config{Routes: []Route{{Name: "history", LocalPrefix: "/history/", Enabled: true, AllowHTTP: true, Upstream: upstream.URL}}}, nil)
	cookie, csrf := loginAdmin(t, s)
	key := issueAdminKey(t, s, cookie, csrf, "Automation")
	c := createMachineClient(t, s, key.Token, "Audited device", []string{"history"}, "")
	forward := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/history/"+path, nil)
		r.Header.Set("Authorization", "Bearer "+c.Token)
		return serveManage(s, r)
	}
	if w := forward("before"); w.Code != 204 {
		t.Fatal(w.Code)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- forward("slow") }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request not admitted")
	}
	path := "clients/" + c.Client.ID
	proxyResponse[map[string]bool](t, manageRequest(s, "DELETE", path, key.Token, nil), 200)
	proxyResponse[map[string]bool](t, manageRequest(s, "DELETE", path+"?purge=1", key.Token, nil), 200)
	close(done)
	closed = true
	select {
	case w := <-result:
		if w.Code != 204 {
			t.Fatalf("in-flight request failed: %d", w.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete")
	}
	recs, _, err := s.store.records(nil, 10)
	if err != nil || len(recs) != 2 {
		t.Fatalf("request history disappeared: %v", err)
	}
	for _, r := range recs {
		if r.Status != 204 || r.CallerName != c.Client.Name || r.CallerID != c.Client.ID {
			t.Fatalf("request attribution lost: %+v", r)
		}
		var payload string
		if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", r.ID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var m MoneyRecord
		if err := json.Unmarshal([]byte(payload), &m); err != nil || m.Device != c.Client.Name || m.DeviceID != c.Client.ID {
			t.Fatalf("money attribution lost: %+v %v", m, err)
		}
	}
}

func TestCallerRetentionAcrossCreatePaths(t *testing.T) {
	s := testService(t)
	configure(t, s, Config{Routes: []Route{{Name: "fixture", LocalPrefix: "/fixture/", Enabled: true, Upstream: "https://example.test"}}}, nil)
	execProxySQL(t, s.store, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<4096)
 INSERT INTO callers(id,name,hash,created,revoked) SELECT 'old-'||i,'Old','hash-'||i,i,1 FROM n`)
	execProxySQL(t, s.store, "INSERT INTO grants VALUES('old-1','fixture','fixture-authority'),('old-2','fixture','fixture-authority')")
	seedManageEnrollment(t, s, "old-1", "fixture")
	seedManageEnrollment(t, s, "old-2", "fixture")
	// An invalid grant after automatic deletion must roll back the eviction.
	if _, _, err := s.store.createCaller("Invalid", []string{"unknown"}, s.snap); err == nil {
		t.Fatal("invalid grant accepted")
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment") != 2 {
		t.Fatal("failed create evicted retained caller")
	}
	// Claude setup and ordinary browser create use the same cap and ordering.
	next := *s.snap
	next.revision++
	if err := s.store.createClaudeSetup(context.Background(), &next, nil, "fixture", "native-fixture", "synthetic"); err != nil {
		t.Fatal(err)
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE id='old-1'") != 0 || proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE id='old-2'") != 1 {
		t.Fatal("Claude create did not evict the first oldest revoked caller")
	}
	for range maxActiveCallers + 1 {
		c, _, err := s.store.createCaller("Churn", []string{"fixture"}, s.snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.revokeCaller(context.Background(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if proxyCount(t, s.store, "SELECT count(*) FROM callers") != maxRetainedCallers || proxyCount(t, s.store, "SELECT count(*) FROM callers WHERE revoked=0") != 1 || proxyCount(t, s.store, "SELECT count(*) FROM grants WHERE caller IN ('old-1','old-2')") != 0 || proxyCount(t, s.store, "SELECT count(*) FROM claude_shared_enrollment") != 0 {
		t.Fatal("retained rows not bounded or dependents not evicted")
	}
	execProxySQL(t, s.store, `UPDATE callers SET revoked=0 WHERE id IN (SELECT id FROM callers WHERE revoked=1 LIMIT 1023)`)
	if _, _, err := s.store.createCaller("At capacity", nil, s.snap); !errors.Is(err, callerLimitError) {
		t.Fatalf("active limit not enforced: %v", err)
	}
	next.revision++
	if err := s.store.createClaudeSetup(context.Background(), &next, nil, "fixture", "second-native", "synthetic"); !errors.Is(err, inputError("Client limit reached (1024)")) {
		t.Fatalf("Claude active limit not enforced: %v", err)
	}
}
