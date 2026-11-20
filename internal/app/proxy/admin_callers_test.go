package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAdminPurgeRevokedCaller(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	s := testService(t)
	r := route("r", "/r/", upstream.URL, "X-Local", "key")
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": {Value: "vendor-test-key"}})
	c, token, err := s.store.createCaller("Browser client", []string{"r"}, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAdmin(t, s)
	remove := func(body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		rr := adminRequest(s, http.MethodDelete, "callers", body, cookie, csrf)
		if rr.Code != status {
			t.Fatalf("delete %v: got %d, want %d: %s", body, rr.Code, status, rr.Body)
		}
		return rr
	}
	purge := map[string]any{"id": c.ID, "purge": true}
	if res := remove(purge, http.StatusConflict); !strings.Contains(res.Body.String(), "Revoke the client") {
		t.Fatalf("conflict should explain how to delete the client: %s", res.Body)
	}
	if rr := call(s, http.MethodGet, "/r/item", "X-Local", token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("active caller lost access: %d %s", rr.Code, rr.Body)
	}

	requests := func() requestPage {
		t.Helper()
		return decodeNumberedPage(t, adminRequest(s, http.MethodGet, "requests?page=1&caller="+c.ID, nil, cookie, csrf))
	}
	before := requests()
	if len(before.Requests) != 1 || before.Requests[0].CallerID != c.ID || before.Requests[0].CallerName != c.Name {
		t.Fatalf("missing request snapshot: %+v", before)
	}
	money := func() string {
		t.Helper()
		var data string
		if err := s.store.db.QueryRow("SELECT data FROM money_history WHERE request_id=? AND caller=?", before.Requests[0].ID, c.ID).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	moneyBefore := money()

	remove(map[string]string{"id": c.ID}, http.StatusOK)
	remove(purge, http.StatusOK)
	remove(purge, http.StatusNotFound)
	if rr := call(s, http.MethodGet, "/r/item", "X-Local", token, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("purged token authenticated: %d %s", rr.Code, rr.Body)
	}
	if after := requests(); !reflect.DeepEqual(before, after) {
		t.Fatalf("Requests page lost history or attribution: before %+v, after %+v", before, after)
	}
	if after := money(); moneyBefore != after {
		t.Fatalf("money snapshot changed after purge: before %s, after %s", moneyBefore, after)
	}
	var grants int
	if err := s.store.db.QueryRow("SELECT count(*) FROM grants WHERE caller=?", c.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("purged client still has grants: count=%d err=%v", grants, err)
	}
}

func TestAdminPurgeAllRevokedCallers(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	cookie, csrf := loginAdmin(t, s)
	want := map[string]bool{}
	for _, key := range []string{"", "ak_test"} {
		for _, revoked := range []bool{false, true} {
			c, _, err := s.store.insertCaller(ctx, "Client", nil, s.snap, key, "")
			if err != nil {
				t.Fatal(err)
			}
			if !revoked {
				want[c.ID] = true
				continue
			}
			if err := s.store.revokeCaller(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	bulk := map[string]bool{"purge_revoked": true}
	if rr := adminRequest(s, http.MethodDelete, "callers", map[string]bool{"purge_revoked": true, "purge": true}, cookie, csrf); rr.Code != http.StatusBadRequest {
		t.Fatalf("conflicting delete flags: %d %s", rr.Code, rr.Body)
	}
	for range 2 { // An empty bulk purge is also successful.
		if rr := adminRequest(s, http.MethodDelete, "callers", bulk, cookie, csrf); rr.Code != http.StatusOK {
			t.Fatalf("bulk purge: %d %s", rr.Code, rr.Body)
		}
	}
	remaining, err := s.store.callers()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != len(want) {
		t.Fatalf("expected only active callers after bulk purge, got %+v", remaining)
	}
	for _, c := range remaining {
		if !want[c.ID] || c.Revoked {
			t.Errorf("unexpected caller after bulk purge: %+v", c)
		}
	}
}
