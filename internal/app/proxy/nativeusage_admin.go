package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage/poll"
)

func (s *Service) nativeUsageAPI(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "usage" && r.Method == http.MethodGet:
		s.mu.RLock() // Do not observe an in-progress configuration publication.
		current := s.nativeUsage.Snapshot()
		s.mu.RUnlock()
		jsonReply(w, http.StatusOK, current)
	case path == "usage/refresh" && r.Method == http.MethodPost:
		// Standalone Run starts this automatically; an embedded Service can
		// opt into polling with its first authenticated manual refresh.
		s.mu.RLock()
		s.nativeUsage.Start(context.Background())
		err := s.nativeUsage.Refresh()
		s.mu.RUnlock()
		nativeUsageMutation(w, err)
	case path == "usage/settings" && r.Method == http.MethodPut:
		var in struct {
			IntervalMinutes int `json:"interval_minutes"`
		}
		if readJSON(w, r, &in) {
			nativeUsageMutation(w, s.nativeUsage.SetInterval(in.IntervalMinutes))
		}
	default:
		return false
	}
	return true
}

func nativeUsageMutation(w http.ResponseWriter, err error) {
	var cooldown *poll.CooldownError
	switch {
	case err == nil:
		jsonReply(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.As(err, &cooldown):
		seconds := int((cooldown.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		fail(w, http.StatusTooManyRequests, "Usage refresh is on cooldown. Try again in "+strconv.Itoa(seconds)+"s.")
	case errors.Is(err, poll.ErrInterval):
		fail(w, http.StatusBadRequest, poll.ErrInterval.Error())
	case errors.Is(err, poll.ErrClosed):
		fail(w, http.StatusServiceUnavailable, "Proxy is shutting down")
	default:
		fail(w, http.StatusServiceUnavailable, "Could not save usage settings; active interval unchanged")
	}
}
