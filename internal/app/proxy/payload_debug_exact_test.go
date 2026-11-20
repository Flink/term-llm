package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPayloadDebugExactRequestLookup(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	lookup := func(id string) *httptest.ResponseRecorder {
		return adminRequest(s, "GET", "debug/captures/"+id, nil, cookie, csrf)
	}
	if out := lookup("px_missing"); out.Code != 404 {
		t.Fatal(out.Code, out.Body.String())
	}
	if _, err := os.Stat(s.payloadDebugDirectory()); !os.IsNotExist(err) {
		t.Fatal("lookup created archive", err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "PRIVATE-RESPONSE")
	}))
	defer up.Close()
	if out := adminRequest(s, "PUT", "secrets", map[string]string{"name": "key", "value": "PRIVATE-HEADER"}, cookie, csrf); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	configure(t, s, Config{Routes: []Route{route("r", "/r/", up.URL, "X-Local", "key")}}, map[string]material{"key": boundMaterial(t, up.URL, "PRIVATE-HEADER")})
	token := caller(t, s, "r")
	send := func() string {
		out := call(s, "POST", "/r/anything?value=PRIVATE-QUERY", "X-Local", token, strings.NewReader("PRIVATE-REQUEST"))
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
		return out.Header().Get("X-Term-LLM-Proxy-Request-ID")
	}
	enableDebugAdmin(t, s, cookie, csrf)
	id := send()
	for i := range 51 {
		rec, req := debugRequest(fmt.Sprintf("px_new_%02d", i))
		rec.Started = 1<<62 + int64(i)
		s.beginPayloadCapture(rec, req).finish(rec)
	}
	if out := adminRequest(s, "GET", "debug/captures", nil, cookie, csrf); out.Code != 200 || strings.Contains(out.Body.String(), id) {
		t.Fatal("fixture must fall outside default list window", out.Code, out.Body.String())
	}
	if err := s.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	uncaptured := send()
	if out := lookup(uncaptured); out.Code != 404 {
		t.Fatal(out.Code, out.Body.String())
	}
	assertExact := func() {
		t.Helper()
		out := lookup(id)
		if want := fmt.Sprintf(`{"files":["request.body","response.body"],"finished":true,"id":%q}`, id); out.Code != 200 || strings.TrimSpace(out.Body.String()) != want {
			t.Fatal(out.Code, out.Body.String())
		}
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("metadata cacheable", out.Header())
		}
		preview := lookup(id + "/request.body?limit=7")
		if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"content":"PRIVATE"`) || preview.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(preview.Code, preview.Body.String())
		}
		ledger := adminRequest(s, "GET", "requests?id="+id, nil, cookie, csrf)
		var page struct{ Requests []Record }
		if err := json.Unmarshal(ledger.Body.Bytes(), &page); err != nil || len(page.Requests) != 1 || page.Requests[0].ID != id || strings.Contains(ledger.Body.String(), "PRIVATE-") {
			t.Fatal(ledger.Code, ledger.Body.String(), err)
		}
	}
	assertExact()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cookie, csrf = loginAdmin(t, s)
	// An unrelated non-record entry would fail a capture listing. Exact
	// reads inspect only this record, even on the first request after restart.
	if err := os.WriteFile(filepath.Join(s.payloadDebugDirectory(), "unrelated"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	assertExact()
	if s.payloadDebug != nil {
		t.Fatal("exact reads initialized capture")
	}
}

func TestPayloadDebugExactRequestLookupEdgeCases(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	enableDebugAdmin(t, s, cookie, csrf)
	const id = "px_exact"
	rec, req := debugRequest(id)
	capture := s.beginPayloadCapture(rec, req)
	expect := func(path string, code int, body string) {
		t.Helper()
		out := adminRequest(s, "GET", "debug/captures/"+path, nil, cookie, csrf)
		if out.Code != code || !strings.Contains(out.Body.String(), body) {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	expect("", 400, "Invalid capture ID")
	expect("%2e%2e", 400, "Invalid capture ID")
	expect("px_other", 404, "not found")
	expect(id, 200, `"files":[],"finished":false`)
	capture.observe("response.body", []byte("private"), false, false, false)
	expect(id, 200, `"files":["response.body"],"finished":false`)
	capture.finish(rec)
	if err := os.Truncate(filepath.Join(capture.dir, "response.body"), 0); err != nil {
		t.Fatal(err)
	}
	expect(id, 200, `"files":[],"finished":true`)
	manifest := filepath.Join(capture.dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"id":"px_wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	expect(id, 404, "not found")
	expect(id+"/response.body", 404, "not found")
	if err := os.RemoveAll(capture.dir); err != nil {
		t.Fatal(err)
	}
	expect(id, 404, "not found")
}
