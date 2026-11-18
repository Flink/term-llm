package proxy

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/term-llm/term-llm/internal/secrettext"
)

const (
	adminKeyPrefix = "sk-tlpadm-"
	maxAdminKeys   = 32
)

var errAdminKeyLimit = errors.New("Admin API key limit reached (32)")

type AdminKey struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Created  int64  `json:"created"`
	LastUsed int64  `json:"last_used"`
}

// The bounded hash registry is only a hint to let real keys from throttled
// peers reach SQLite. It never authenticates a request. Its own lock lets the
// body preflight run without the snapshot lock. Writers publish membership
// changes only after the transaction commits, still under s.mu.
type adminKeyRegistry struct {
	mu   sync.RWMutex
	uses map[string]*adminKeyUse
}

type adminKeyUse struct{ lastUsed atomic.Int64 }

func (reg *adminKeyRegistry) get(hash string) *adminKeyUse {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.uses[hash]
}

func (reg *adminKeyRegistry) set(hash string, use *adminKeyUse) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if use == nil {
		delete(reg.uses, hash)
	} else {
		reg.uses[hash] = use
	}
}

func (s *store) loadAdminKeyUses() (map[string]*adminKeyUse, error) {
	rows, err := s.db.Query("SELECT hash,last_used FROM admin_keys LIMIT ?", maxAdminKeys+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*adminKeyUse)
	for rows.Next() {
		var hash string
		var used int64
		if err := rows.Scan(&hash, &used); err != nil {
			return nil, err
		}
		u := &adminKeyUse{}
		u.lastUsed.Store(used)
		out[hash] = u
	}
	if len(out) > maxAdminKeys {
		return nil, errAdminKeyLimit
	}
	return out, rows.Err()
}

func (s *store) adminKeys() ([]AdminKey, error) {
	rows, err := s.db.Query("SELECT id,name,created,last_used FROM admin_keys ORDER BY created,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminKey{}
	for rows.Next() {
		var k AdminKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Created, &k.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *store) createAdminKey(ctx context.Context, key AdminKey, hash string) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM admin_keys").Scan(&count); err != nil {
			return err
		}
		if count >= maxAdminKeys {
			return errAdminKeyLimit
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO admin_keys(id,name,hash,created) VALUES(?,?,?,?)", key.ID, key.Name, hash, key.Created)
		return err
	})
}

func (s *store) deleteAdminKey(ctx context.Context, id string, cascade bool) (key AdminKey, hash string, revoked []string, err error) {
	err = s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "DELETE FROM admin_keys WHERE id=? RETURNING id,name,hash", id).Scan(&key.ID, &key.Name, &hash); err != nil {
			return err
		}
		if !cascade {
			return nil
		}
		var err error
		revoked, err = selectStrings(ctx, tx, "SELECT id FROM callers WHERE created_by=? AND revoked=0", id)
		if err != nil {
			return err
		}
		for _, caller := range revoked {
			if err := revokeCallerTx(ctx, tx, caller); err != nil {
				return err
			}
		}
		return nil
	})
	return
}

func (s *Service) createAdminKey(w http.ResponseWriter, r *http.Request, sess session) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if len(name) < 1 || len(name) > 80 {
		fail(w, 400, "Enter an admin API key name (1–80 characters)")
		return
	}
	if s.passkey != nil && !s.consumeRecentAuth(w, sess) {
		return
	}
	key := AdminKey{ID: randomToken("ak_")[:26], Name: name, Created: time.Now().UnixMilli()}
	token := randomToken(adminKeyPrefix)
	hash := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.createAdminKey(r.Context(), key, hash); err != nil {
		if errors.Is(err, errAdminKeyLimit) {
			fail(w, 409, err.Error())
		} else {
			fail(w, 503, "Could not issue admin API key")
		}
		return
	}
	s.adminKeyUses.set(hash, &adminKeyUse{})
	log.Printf("proxy: admin_key_issue key=%s name=%q cascade=0", key.ID, adminAuditName(key.Name))
	jsonReply(w, 201, map[string]any{"key": key, "token": token})
}

func (s *Service) revokeAdminKey(w http.ResponseWriter, r *http.Request, sess session) {
	var in struct {
		ID            string `json:"id"`
		RevokeClients bool   `json:"revoke_clients"`
	}
	if !readJSON(w, r, &in) || s.passkey != nil && !s.consumeRecentAuth(w, sess) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, hash, revoked, err := s.store.deleteAdminKey(r.Context(), in.ID, in.RevokeClients)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "Admin API key not found")
		} else {
			fail(w, 503, "Could not revoke admin API key")
		}
		return
	}
	s.adminKeyUses.set(hash, nil)
	for _, id := range revoked {
		s.cancelCallerSockets(id)
	}
	log.Printf("proxy: admin_key_revoke key=%s name=%q cascade=%d", key.ID, adminAuditName(key.Name), len(revoked))
	jsonReply(w, 200, map[string]any{"ok": true, "revoked_clients": len(revoked)})
}

func adminAuditName(name string) string {
	// A pasted proxy token can appear in an otherwise valid user-chosen name,
	// even without a word boundary (which the general redactor requires).
	if strings.Contains(name, "sk-tlp") || strings.Contains(name, "admin_") {
		return "[redacted]"
	}
	return secrettext.Redact(name)
}
