package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"
)

func seedPaginationRecords(t *testing.T, s *store, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		r := Record{
			ID: fmt.Sprintf("page-%03d", i), Started: int64(1000 + i),
			CallerID: "odd", Route: "other", Method: "GET",
			Path: fmt.Sprintf("v1/item-%03d", i), LocalPrefix: "/local/",
			Outcome: "unknown",
		}
		if i%2 == 0 {
			r.CallerID, r.Method = "even", "POST"
		}
		if i%3 == 0 {
			r.Route = "three"
		}
		if err := s.start(context.Background(), &r); err != nil {
			t.Fatal(err)
		}
		r.Ended, r.Outcome = r.Started+1, "complete"
		if i%4 == 0 {
			r.Outcome = "failed"
		}
		if err := s.finish(r); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeNumberedPage(t *testing.T, rr *httptest.ResponseRecorder) requestPage {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("numbered response %d: %s", rr.Code, rr.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"requests", "next", "total", "page", "page_size"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing numbered response field %q: %s", key, rr.Body.String())
		}
	}
	if string(fields["requests"]) == "null" {
		t.Error("requests must be an array, including for an empty history")
	}
	var page requestPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func checkNumberedPage(t *testing.T, got requestPage, total, page, size int64, count int, first int64) {
	t.Helper()
	if got.Total != total || got.Page != page || got.PageSize != size || len(got.Requests) != count {
		t.Fatalf("got total=%d page=%d size=%d rows=%d; want %d %d %d %d", got.Total, got.Page, got.PageSize, len(got.Requests), total, page, size, count)
	}
	for i, r := range got.Requests {
		if r.Seq != first-int64(i) || r.ID != fmt.Sprintf("page-%03d", r.Seq) || r.Path != fmt.Sprintf("/v1/item-%03d", r.Seq) {
			t.Errorf("row %d: unexpected record %+v", i, r)
		}
	}
	var next int64
	if count > 0 && page*size < total {
		next = got.Requests[count-1].Seq
	}
	if got.Next != next {
		t.Errorf("next=%d, want %d", got.Next, next)
	}
}

func TestNumberedRequestPages(t *testing.T) {
	for _, count := range []int{0, 50, 63} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			s := testService(t)
			seedPaginationRecords(t, s.store, count)
			cookie, csrf := loginAdmin(t, s)
			for _, test := range []struct {
				name string
				page int64
				size int64
			}{
				{"first default size", 1, 25},
				{"second full page", 2, 25},
				{"last or clamped", 3, 25},
				{"direct random jump", 8, 7},
				{"largest page", math.MaxInt64, 25},
				{"minimum size", 23, 1},
				{"maximum size", 1, 100},
			} {
				t.Run(test.name, func(t *testing.T) {
					last := int64(1)
					if count > 0 {
						last = (int64(count)-1)/test.size + 1
					}
					page := min(test.page, last)
					first := int64(count) - (page-1)*test.size
					rows := int(min(first, test.size))
					stored, err := s.store.recordsPage(context.Background(), nil, test.page, test.size)
					if err != nil {
						t.Fatal(err)
					}
					checkNumberedPage(t, stored, int64(count), page, test.size, rows, first)
					query := fmt.Sprintf("requests?page=%d", test.page)
					if test.size != defaultRequestPageSize {
						query += fmt.Sprintf("&page_size=%d", test.size)
					}
					response := decodeNumberedPage(t, adminRequest(s, "GET", query, nil, cookie, csrf))
					checkNumberedPage(t, response, int64(count), page, test.size, rows, first)
				})
			}
		})
	}
}

func TestNumberedRequestFilterTotals(t *testing.T) {
	s := testService(t)
	seedPaginationRecords(t, s.store, 60)
	cookie, csrf := loginAdmin(t, s)
	for _, test := range []struct {
		query string
		total int64
	}{
		{"id=page-021", 1},
		{"id=missing", 0},
		{"caller=even", 30},
		{"route=three", 20},
		{"method=POST", 30},
		{"outcome=failed", 15},
		{"since=1021", 40},
		{"until=1020", 20},
		{"since=1021&until=1030", 10},
		{"path=/local/v1/", 60},
		{"path=/v1/item-001", 1},
		{"caller=even&route=three&outcome=failed", 5},
		{"route=three&outcome=complete", 15},
		{"caller=even&method=POST", 30},
		{"since=1021&until=1030&outcome=complete", 8},
		{"id=page-022&method=POST", 1},
	} {
		t.Run(test.query, func(t *testing.T) {
			q, err := url.ParseQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}
			legacy, _, err := s.store.records(q, 100)
			if err != nil || len(legacy) != int(test.total) {
				t.Fatalf("legacy filter rows=%d want=%d err=%v", len(legacy), test.total, err)
			}
			var combined []Record
			for page := int64(1); ; page++ {
				stored, err := s.store.recordsPage(context.Background(), q, page, 7)
				if err != nil {
					t.Fatal(err)
				}
				api := decodeNumberedPage(t, adminRequest(s, "GET", fmt.Sprintf("requests?%s&page=%d&page_size=7", test.query, page), nil, cookie, csrf))
				if stored.Total != test.total || api.Total != test.total || !reflect.DeepEqual(api, stored) {
					t.Fatalf("filter totals or records disagree: store=%+v API=%+v", stored, api)
				}
				combined = append(combined, stored.Requests...)
				if stored.Next == 0 {
					break
				}
			}
			if len(combined) != len(legacy) || (len(legacy) > 0 && !reflect.DeepEqual(combined, legacy)) {
				t.Fatal("numbered pages must contain exactly the same filtered records as the cursor query")
			}
		})
	}
}

func TestNumberedRequestInvalidInputs(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	for _, query := range []string{
		"page=", "page", "page=0", "page=-1", "page=1.5", "page=abc", "page=%201",
		"page=9223372036854775808", "page=9999999999999999999999999999999999999", "page=1&page=2",
		"page=1&page_size=", "page=1&page_size=0", "page=1&page_size=-1", "page=1&page_size=101",
		"page=1&page_size=1.5", "page=1&page_size=abc", "page=1&page_size=9223372036854775807",
		"page=1&page_size=9223372036854775808", "page=1&page_size=25&page_size=25",
		"page=1&before=2", "page=1&before=", "page=1&since=bad", "page=1&until=bad",
	} {
		t.Run(query, func(t *testing.T) {
			rr := adminRequest(s, "GET", "requests?"+query, nil, cookie, csrf)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("invalid query returned %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
	for _, test := range []struct{ page, size int64 }{{0, 25}, {-1, 25}, {1, 0}, {1, 101}, {1, math.MaxInt64}} {
		if _, err := s.store.recordsPage(context.Background(), nil, test.page, test.size); err == nil {
			t.Errorf("store accepted invalid page=%d size=%d", test.page, test.size)
		}
	}
	if _, err := s.store.recordsPage(context.Background(), url.Values{"before": {""}}, 1, 25); err == nil {
		t.Error("store accepted before with numbered pagination")
	}
}

func TestNumberedRequestActiveUnknownCounts(t *testing.T) {
	s := testService(t)
	seedPaginationRecords(t, s.store, 2)
	for _, id := range []string{"active-a", "active-b", "old-unknown"} {
		r := Record{ID: id, Outcome: "unknown"}
		if err := s.store.start(context.Background(), &r); err != nil {
			t.Fatal(err)
		}
	}
	s.active.Store("active-a", true)
	s.active.Store("active-b", true)
	cookie, csrf := loginAdmin(t, s)
	check := func(outcome string, total int64, wantID string) {
		t.Helper()
		// Client-provided active IDs must not affect the count or the rows.
		query := "requests?page=99&page_size=1&outcome=" + outcome + "&_active=old-unknown"
		page := decodeNumberedPage(t, adminRequest(s, "GET", query, nil, cookie, csrf))
		if page.Total != total || page.Page != max(1, total) || page.PageSize != 1 || page.Next != 0 {
			t.Fatalf("%s metadata: %+v", outcome, page)
		}
		if total == 0 {
			if len(page.Requests) != 0 {
				t.Fatalf("expected no %s records: %+v", outcome, page)
			}
		} else if len(page.Requests) != 1 || page.Requests[0].ID != wantID || page.Requests[0].Outcome != outcome {
			t.Fatalf("%s rows: %+v", outcome, page)
		}
	}
	check("active", 2, "active-a")
	check("unknown", 1, "old-unknown")
	s.active.Delete("active-a")
	s.active.Delete("active-b")
	check("active", 0, "")
	check("unknown", 3, "active-a")
}

func TestNumberedPaginationPreservesCursorAndExport(t *testing.T) {
	s := testService(t)
	seedPaginationRecords(t, s.store, 63)
	cookie, csrf := loginAdmin(t, s)
	for _, test := range []struct {
		path   string
		count  int
		next   int64
		first  int64
		export bool
	}{
		{"requests", 50, 14, 63, false},
		{"requests?before=14", 13, 0, 13, false},
		{"requests?page_size=1", 50, 14, 63, false},
		{"requests/export", 63, 0, 63, true},
		{"requests/export?page=2&page_size=1", 63, 0, 63, true},
		{"requests/export?page=bad&page_size=0&before=14", 13, 0, 13, true},
	} {
		t.Run(test.path, func(t *testing.T) {
			rr := adminRequest(s, "GET", test.path, nil, cookie, csrf)
			if rr.Code != http.StatusOK {
				t.Fatalf("legacy response %d: %s", rr.Code, rr.Body.String())
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 2 || fields["requests"] == nil || fields["next"] == nil {
				t.Fatalf("legacy response shape changed: %s", rr.Body.String())
			}
			var result requestPage
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Requests) != test.count || result.Next != test.next || result.Requests[0].Seq != test.first {
				t.Fatalf("legacy rows=%d next=%d first=%d", len(result.Requests), result.Next, result.Requests[0].Seq)
			}
			if test.export && rr.Header().Get("Content-Disposition") != `attachment; filename="proxy-requests.json"` {
				t.Errorf("export attachment changed: %s", rr.Header().Get("Content-Disposition"))
			}
		})
	}
}

func TestNumberedRequestSnapshotDuringAdmission(t *testing.T) {
	s := testService(t)
	seedPaginationRecords(t, s.store, 1)
	// An independent WAL connection can commit even while the reader holds its
	// snapshot, unlike writes through the store's single-connection pool.
	writer, err := openStore(s.opts.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			r := Record{ID: fmt.Sprintf("concurrent-%d", i), Outcome: "unknown"}
			if err := writer.start(context.Background(), &r); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 100; i++ {
		page, err := s.store.recordsPage(context.Background(), nil, 1, 25)
		if err != nil {
			t.Fatal(err)
		}
		// No deletions or rolled-back inserts: count equals the newest sequence.
		if len(page.Requests) != int(min(page.Total, 25)) || page.Requests[0].Seq != page.Total {
			t.Fatalf("count and rows came from different snapshots: total=%d rows=%+v", page.Total, page.Requests)
		}
	}
}
