package proxy

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type manageCredential struct{ hash, peer string }

var adminBearer = regexp.MustCompile(`^Bearer ` + adminKeyPrefix + `[a-f0-9]{64}$`)

func newManageCredential(r *http.Request) manageCredential {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	auth := manageCredential{peer: peer}
	var value string
	count := 0
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			count += len(values)
			if len(values) == 1 {
				value = values[0]
			}
		}
	}
	if count == 1 && adminBearer.MatchString(value) {
		auth.hash = hashToken(strings.TrimPrefix(value, "Bearer "))
	}
	return auth
}

const (
	manageFailureLimit = 8
	managePeerLimit    = 1024
	manageWindow       = time.Minute
)

type manageAttempts struct {
	mu    sync.Mutex
	peers map[string]attempt
}

// Check before lookup; record only an actual authentication failure. Each
// peer is keyed by its socket address without the transient source port, and
// untrusted forwarding headers cannot choose the bucket.
func (m *manageAttempts) limited(peer string, now time.Time, record bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.peers[peer]
	if ok && now.Sub(a.since) >= manageWindow {
		delete(m.peers, peer)
		a, ok = attempt{}, false
	}
	if a.count >= manageFailureLimit {
		return true
	}
	if !ok && len(m.peers) >= managePeerLimit {
		for p, other := range m.peers {
			if now.Sub(other.since) >= manageWindow {
				delete(m.peers, p)
			}
		}
		if len(m.peers) >= managePeerLimit {
			return true
		}
	}
	if record {
		if m.peers == nil {
			m.peers = make(map[string]attempt)
		}
		if !ok {
			a.since = now
		}
		a.count++
		m.peers[peer] = a
	}
	return false
}

func manageTooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	fail(w, 429, "Too many invalid admin API key attempts; wait one minute")
}

func (s *Service) rejectManageKey(w http.ResponseWriter, peer string) {
	if s.manageGate.limited(peer, time.Now(), true) {
		manageTooMany(w)
		return
	}
	fail(w, 401, "Admin API key not recognized")
}

// When touch is true, the caller holds s.mu (write for the final mutation
// authentication, read for GETs). A body preflight passes false and holds no
// snapshot lock.
// An issued hash only bypasses the peer throttle; SQLite is authoritative even
// on a cache hit, including when a request queued before the key was revoked.
func (s *Service) validateManage(w http.ResponseWriter, r *http.Request, auth manageCredential, touch bool) (string, bool) {
	if auth.hash == "" {
		s.rejectManageKey(w, auth.peer)
		return "", false
	}
	u := s.adminKeyUses.get(auth.hash)
	if u == nil && s.manageGate.limited(auth.peer, time.Now(), false) {
		manageTooMany(w)
		return "", false
	}
	var key string
	err := s.store.db.QueryRowContext(r.Context(), "SELECT id FROM admin_keys WHERE hash=?", auth.hash).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		s.rejectManageKey(w, auth.peer)
		return "", false
	}
	if err != nil || touch && u != nil && s.touchAdminKey(r, u, key) != nil {
		fail(w, 503, "Management storage unavailable")
		return "", false
	}
	return key, true
}

func (s *Service) touchAdminKey(r *http.Request, u *adminKeyUse, key string) error {
	now := time.Now().UnixMilli()
	old := u.lastUsed.Load()
	if now-old < manageWindow.Milliseconds() || !u.lastUsed.CompareAndSwap(old, now) {
		return nil
	}
	_, err := s.store.db.ExecContext(r.Context(), "UPDATE admin_keys SET last_used=? WHERE id=?", now, key)
	if err != nil {
		u.lastUsed.CompareAndSwap(now, old)
	}
	return err
}
