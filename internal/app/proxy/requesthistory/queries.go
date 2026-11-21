// Package requesthistory builds indexed query predicates for proxy request history.
package requesthistory

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Keep these predicates aligned with the frozen v10 path index; a semantic
// change needs a new migration to replace the index.
const upstreamPath = `CASE WHEN substr(path,1,1)='/' THEN path ELSE '/'||path END`

// A corrupt historical payload must not prevent opening or upgrading the store.
const localPath = `COALESCE(CASE WHEN json_valid(data) THEN json_extract(data,'$.local_prefix') END,'')||ltrim(path,'/')`

// Where builds a parameterized predicate from the admin filters.
func Where(q url.Values) (string, []any, error) {
	where := []string{"1=1"}
	args := []any{}
	for _, key := range []string{"id", "caller", "route"} {
		if v := q.Get(key); v != "" {
			where = append(where, key+"=?")
			args = append(args, v)
		}
	}
	selective := len(args) != 0
	if outcome := q.Get("outcome"); outcome == "active" || outcome == "unknown" {
		ids := q["_active"]
		if len(ids) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
			clause := "id IN (" + placeholders + ")"
			if outcome == "unknown" {
				clause = "NOT (" + clause + ")"
			} else {
				selective = true
			}
			where = append(where, clause)
			for _, id := range ids {
				args = append(args, id)
			}
		} else if outcome == "active" {
			where = append(where, "0=1")
		}
	}
	for _, f := range []struct{ key, clause string }{{"before", "seq < ?"}, {"since", "started >= ?"}, {"until", "started <= ?"}} {
		if v := q.Get(f.key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return "", nil, fmt.Errorf("invalid filter")
			}
			if n > 0 {
				where = append(where, f.clause)
				args = append(args, n)
				selective = selective || f.key != "before"
			}
		}
	}
	// Without planner statistics, SQLite can favor a narrow but low-cardinality
	// index (e.g. outcome=complete) and visit nearly every request. Keep these
	// predicates as residual text comparisons when a selective filter is set.
	for _, key := range []string{"method", "outcome"} {
		if v := q.Get(key); v != "" {
			if key == "outcome" && v == "active" {
				v = "unknown"
			}
			column := key
			if selective {
				column = "+" + column // Disqualify this term from index selection.
			}
			where = append(where, column+"=?")
			args = append(args, v)
		}
	}
	if v := q.Get("path"); v != "" {
		where = append(where, "(instr("+upstreamPath+",?)>0 OR instr("+localPath+",?)>0)")
		args = append(args, v, v)
	}
	return strings.Join(where, " AND "), args, nil
}
