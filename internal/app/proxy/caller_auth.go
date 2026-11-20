package proxy

import (
	"net/http"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func callerTokenHash(token string) (hash, tag string) {
	secret, tag := proxyprofile.SplitCallerToken(token)
	return hashToken(secret), tag
}

func distinctCallerSecret(token string) string {
	if secret, tag := proxyprofile.SplitCallerToken(token); tag != "" {
		return secret
	}
	return ""
}

func (s *store) authorize(token string, r activeRoute) (Caller, error) {
	hash, tag := callerTokenHash(token)
	caller := Caller{Tag: tag}
	err := s.authorizeStmt.QueryRow(hash, r.Name, r.authority).Scan(&caller.ID, &caller.Name)
	return caller, err
}

func (s *store) callerForToken(token string) (Caller, error) {
	hash, tag := callerTokenHash(token)
	caller := Caller{Tag: tag}
	err := s.db.QueryRow("SELECT id,name FROM callers WHERE hash=? AND revoked=0", hash).Scan(&caller.ID, &caller.Name)
	return caller, err
}

// serveClientQuery authenticates metadata requests and holds the snapshot lock
// while the handler enumerates grants. These endpoints don't grant route access.
func (s *Service) serveClientQuery(w http.ResponseWriter, r *http.Request, reply func(http.ResponseWriter, Caller)) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requestSlot(w) {
		return
	}
	defer func() { <-s.slots }()
	token := callerCredential(r.Header, "Authorization", "Bearer ")
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.closing {
		if caller, err := s.store.callerForToken(token); err == nil {
			reply(w, caller)
			return
		}
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func callerCredential(header http.Header, name, prefix string) string {
	if values := header.Values(name); len(values) == 1 {
		if token, ok := strings.CutPrefix(values[0], prefix); ok {
			return token
		}
	}
	return ""
}
