package proxy

import (
	"net/url"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/requesthistory"
)

func TestCallerLastUsedIsIndexedLatestAdmission(t *testing.T) {
	s := testService(t)
	if _, err := s.store.db.Exec(`INSERT INTO callers(id,name,hash,created) VALUES('busy','Busy','busy-hash',1),('unused','Unused','unused-hash',2)`); err != nil {
		t.Fatal(err)
	}
	// Retained history with a backwards wall-clock jump: sequence defines latest admission.
	if _, err := s.store.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2048)
 INSERT INTO requests(id,started,caller,route,method,path,outcome,data)
 SELECT 'r-'||x,4096-x,'busy','route','POST','/opaque','complete','{}' FROM n`); err != nil {
		t.Fatal(err)
	}
	callers, err := s.store.callers()
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 2 || callers[0].LastUsed != 2048 || callers[1].LastUsed != 0 {
		t.Fatalf("latest admission and unused caller: %+v", callers)
	}
	rows, err := s.store.db.Query("EXPLAIN QUERY PLAN " + callersQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "requests_caller") {
			indexed = true
		}
		if strings.Contains(detail, "SCAN r") {
			t.Fatalf("unbounded request-history scan: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("last-used lookup must use requests_caller index")
	}
}

func TestRequestPageQueryPlans(t *testing.T) {
	s := testService(t)
	for _, tc := range []struct {
		filter, index string
		values        []any
	}{
		{"method=?", "requests_method", []any{"GET"}},
		{"outcome=?", "requests_outcome", []any{"failed"}},
		{"caller=?", "requests_caller", []any{"client"}},
		{"route=?", "requests_route", []any{"provider"}},
		{"started>=? AND started<=?", "requests_time", []any{123, 456}},
	} {
		t.Run(tc.index, func(t *testing.T) {
			plan := explainRequestQuery(t, s.store, requestPageQuery(tc.filter), append(tc.values, 25, 100)...)
			if !strings.Contains(plan, "SEARCH requests USING COVERING INDEX "+tc.index) ||
				!strings.Contains(plan, "SEARCH requests USING INTEGER PRIMARY KEY") {
				t.Fatalf("page should locate IDs in the filter index before loading payloads:\n%s", plan)
			}
			count := explainRequestQuery(t, s.store, "SELECT count(*) FROM requests WHERE "+tc.filter, tc.values...)
			if !strings.Contains(count, "SEARCH requests USING COVERING INDEX "+tc.index) {
				t.Fatalf("count should use the filter index:\n%s", count)
			}
		})
	}
	plan := explainRequestQuery(t, s.store, retainRequestsQuery, 123)
	if !strings.Contains(plan, "SEARCH requests USING COVERING INDEX requests_time") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("retention should seek directly to the oldest eligible batch:\n%s", plan)
	}
	where, args, err := requesthistory.Where(url.Values{"path": {"/provider/models"}})
	if err != nil {
		t.Fatal(err)
	}
	plan = explainRequestQuery(t, s.store, "SELECT count(*) FROM requests WHERE "+where, args...)
	if !strings.Contains(plan, "SCAN requests USING COVERING INDEX requests_paths") {
		t.Fatalf("substring count should scan the path index rather than the JSON payloads:\n%s", plan)
	}
}

func TestCombinedRequestFilterQueryPlans(t *testing.T) {
	s := testService(t)
	for _, tc := range []struct{ query, index string }{
		{"route=provider&outcome=complete", "requests_route"},
		{"caller=client&method=POST", "requests_caller"},
		{"since=123&until=456&outcome=complete", "requests_time"},
		{"id=request&method=POST", "sqlite_autoindex_requests_1"},
		{"since=0&until=-1&outcome=complete", "requests_outcome"},
		{"before=100&method=POST", "requests_method"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			where, args, err := requesthistory.Where(q)
			if err != nil {
				t.Fatal(err)
			}
			for _, plan := range []string{
				explainRequestQuery(t, s.store, "SELECT count(*) FROM requests WHERE "+where, args...),
				explainRequestQuery(t, s.store, requestPageQuery(where), append(args, 25, 100)...),
				explainRequestQuery(t, s.store, "SELECT seq,data FROM requests WHERE "+where+" ORDER BY seq DESC LIMIT ?", append(args, 25)...),
			} {
				if !strings.Contains(plan, "INDEX "+tc.index+" (") {
					t.Fatalf("expected a search using %s:\n%s", tc.index, plan)
				}
			}
		})
	}
}

func explainRequestQuery(t *testing.T, s *store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}
