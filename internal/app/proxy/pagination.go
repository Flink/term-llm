package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const defaultRequestPageSize int64 = 25

// requestPage keeps numbered metadata mandatory, including for an empty history.
// Next remains a sequence cursor for compatibility with cursor-based readers.
type requestPage struct {
	Requests []Record `json:"requests"`
	Next     int64    `json:"next"`
	Total    int64    `json:"total"`
	Page     int64    `json:"page"`
	PageSize int64    `json:"page_size"`
}

func requestPagination(q url.Values) (int64, int64, error) {
	if q.Has("before") {
		return 0, 0, fmt.Errorf("before cannot be combined with page")
	}
	page, err := positivePageValue(q, "page")
	if err != nil {
		return 0, 0, err
	}
	size := defaultRequestPageSize
	if q.Has("page_size") {
		size, err = positivePageValue(q, "page_size")
		if err != nil {
			return 0, 0, err
		}
	}
	if size > 100 {
		return 0, 0, fmt.Errorf("page_size must be between 1 and 100")
	}
	return page, size, nil
}

func positivePageValue(q url.Values, key string) (int64, error) {
	values := q[key]
	if len(values) == 1 {
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err == nil && n >= 1 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s must be a single positive integer within int64 range", key)
}

// recordsPage counts and reads in one SQLite snapshot so concurrent admission,
// completion, or retention cannot make this response's rows and total disagree.
// Each call gets a fresh snapshot; page positions may change between requests.
func (s *store) recordsPage(ctx context.Context, q url.Values, page, size int64) (requestPage, error) {
	out := requestPage{Page: page, PageSize: size}
	if page < 1 || size < 1 || size > 100 || q.Has("before") {
		return out, fmt.Errorf("invalid numbered pagination")
	}
	where, args, err := requestWhere(q)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin request page: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM requests WHERE "+where, args...).Scan(&out.Total); err != nil {
		return out, fmt.Errorf("count requests: %w", err)
	}
	last := int64(1)
	if out.Total > 0 {
		last = (out.Total-1)/size + 1
	}
	if out.Page > last {
		out.Page = last
	}
	// Clamp before multiplication: even a MaxInt64 page cannot overflow OFFSET.
	offset := (out.Page - 1) * size
	args = append(args, size, offset)
	rows, err := tx.QueryContext(ctx, "SELECT seq,data FROM requests WHERE "+where+" ORDER BY seq DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return out, fmt.Errorf("query request page: %w", err)
	}
	out.Requests, _, err = readRequestRecords(rows, int(size))
	closeErr := rows.Close()
	if err != nil {
		return out, fmt.Errorf("read request page: %w", err)
	}
	if closeErr != nil {
		return out, fmt.Errorf("close request page: %w", closeErr)
	}
	if out.Page < last {
		out.Next = out.Requests[len(out.Requests)-1].Seq
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit request page: %w", err)
	}
	return out, nil
}

func (s *Service) numberedRequests(w http.ResponseWriter, r *http.Request, q url.Values) {
	page, size, err := requestPagination(q)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	result, err := s.store.recordsPage(r.Context(), q, page, size)
	if err != nil {
		fail(w, 400, "Could not read requests: check filters or store readiness")
		return
	}
	s.requestOutcomes(result.Requests)
	jsonReply(w, 200, result)
}
