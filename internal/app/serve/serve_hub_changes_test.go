package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func getHubChanges(t *testing.T, h http.Handler, query string) (int, uint64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/changes"+query, nil))
	var body struct {
		Version uint64 `json:"version"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Version
}

func TestHubChangesLongPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHubServer(nil, nil)
		h := s.handler()

		if code, v := getHubChanges(t, h, ""); code != http.StatusOK || v != 0 {
			t.Fatalf("no since: code=%d version=%d", code, v)
		}
		// A stale version answers immediately (covers a hub restart reset).
		start := time.Now()
		if _, v := getHubChanges(t, h, "?since=7&timeout=30"); v != 0 || time.Since(start) != 0 {
			t.Fatalf("stale since: version=%d after %v", v, time.Since(start))
		}
		// Current version with no change waits out the timeout.
		start = time.Now()
		if _, v := getHubChanges(t, h, "?since=0&timeout=1"); v != 0 || time.Since(start) != time.Second {
			t.Fatalf("timeout: version=%d after %v", v, time.Since(start))
		}
		// A bump wakes a parked poll without advancing the clock.
		type result struct {
			code    int
			version uint64
		}
		done := make(chan result, 1)
		start = time.Now()
		go func() {
			code, v := getHubChanges(t, h, "?since=0&timeout=30")
			done <- result{code, v}
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("poll returned before the bump")
		default:
		}
		s.changes.Bump()
		if r := <-done; r.code != http.StatusOK || r.version != 1 || time.Since(start) != 0 {
			t.Fatalf("bump: code=%d version=%d after %v", r.code, r.version, time.Since(start))
		}
		// Close makes subsequent polls return immediately.
		s.changes.Close()
		start = time.Now()
		if _, v := getHubChanges(t, h, "?since=1&timeout=30"); v != 1 || time.Since(start) != 0 {
			t.Fatalf("closed: version=%d after %v", v, time.Since(start))
		}
		if code, _ := getHubChanges(t, h, "?since=x"); code != http.StatusBadRequest {
			t.Fatalf("bad since: code=%d", code)
		}
	})
}
