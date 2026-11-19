package proxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// removeAccountLocked deletes an account no route references, freeing its name
// (for example to recreate a deleted ChatGPT route). Sign-out keeps the record
// for reconnect; removal does not. The caller holds the account lock and s.mu.
func (s *Service) removeAccountLocked(w http.ResponseWriter, r *http.Request, old accountRecord) {
	if _, managed := s.snap.config.Accounts[old.Name]; managed {
		fail(w, 409, "This account is operator-managed; remove it from the mounted file and Reload")
		return
	}
	var used []string
	for _, route := range s.snap.config.Routes {
		if route.Account == old.Name {
			used = append(used, route.Name)
		}
	}
	if len(used) > 0 {
		fail(w, 409, "Delete routes "+strings.Join(used, ", ")+" before removing this account")
		return
	}
	if err := s.store.deleteAccount(r.Context(), old.Name, old.Generation); err != nil {
		fail(w, 503, "Could not remove account")
		return
	}
	s.accountRejected.Delete(old.Name)
	s.cancelAccountSockets(old.Name)
	jsonReply(w, 200, map[string]any{"removed": old.Name})
}

func (s *store) deleteAccount(ctx context.Context, name string, expected int64) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE name=? AND generation=?", name, expected)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("account changed")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM claude_vault WHERE account=?", name)
		return err
	})
}
