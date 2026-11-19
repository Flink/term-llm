package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// All inputs are prepared under s.mu before entering accountWrite: the store
// has one SQLite connection, so no db-level helper may run inside this callback.
func (s *store) createNativeSetup(ctx context.Context, next *snapshot, account *accountRecord, route Route, id, token, callerName string, operator bool, grants []string) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		if account != nil {
			if err := insertAccount(ctx, tx, *account); err != nil {
				return err
			}
		}
		if err := saveSetupConfig(ctx, tx, next); err != nil {
			return err
		}
		if err := makeCallerRoom(ctx, tx, inputError("Client limit reached (1024)")); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO callers(id,name,hash,created) VALUES(?,?,?,?)", id, callerName, hashToken(token), time.Now().UnixMilli()); err != nil {
			return err
		}
		if err := writeGrants(tx, id, grants, next); err != nil {
			return err
		}
		if operator {
			result, err := tx.ExecContext(ctx, `INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires)
				SELECT ?,caller,route,authority,? FROM grants WHERE caller=? AND route=?
				ON CONFLICT(account) DO UPDATE SET caller=excluded.caller,route=excluded.route,authority=excluded.authority,expires=excluded.expires,reserved=0`, route.Account, time.Now().Add(claudeSharedEnrollmentTTL).Unix(), id, route.Name)
			return claudeSharedRequireChange(result, err)
		}
		return nil
	})
}

// Only config changes here. The canonical money singleton and all secret bytes
// remain untouched; the config mirrors the money value read under s.mu. Account
// bootstrap references and secret references are retained in next.config.
func saveSetupConfig(ctx context.Context, tx *sql.Tx, next *snapshot) error {
	if _, err := reconcileGrants(tx, next); err != nil {
		return err
	}
	data, err := json.Marshal(next.config)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE configuration SET revision=?,data=? WHERE id=1 AND revision=?", next.revision, string(data), next.revision-1)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("configuration revision changed")
	}
	return nil
}

// Clearing a provider removes only its account-owned logins and enrollment grants.
func (s *store) clearNativeSetup(ctx context.Context, next *snapshot, routes []string, p *nativeProxyProvider) ([]string, error) {
	var revoked []string
	err := s.accountWrite(ctx, func(tx *sql.Tx) error {
		const enrollments = " FROM claude_shared_enrollment WHERE account IN (SELECT name FROM accounts WHERE kind=?)"
		affected := map[string]bool{}
		if err := nativeSetupAffectedCallers(ctx, tx, "SELECT caller"+enrollments, []any{p.kind}, affected); err != nil {
			return err
		}
		for _, route := range routes {
			if err := nativeSetupAffectedCallers(ctx, tx, "SELECT caller FROM grants WHERE route=?", []any{route}, affected); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM grants WHERE route=?", route); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE"+enrollments, p.kind); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE kind=?", p.kind); err != nil {
			return err
		}
		if err := saveSetupConfig(ctx, tx, next); err != nil {
			return err
		}
		// Reconciliation can also remove an orphaned native grant. Evaluate
		// remaining access afterwards, not against its pre-clear stale binding.
		for id := range affected {
			result, err := tx.ExecContext(ctx, "UPDATE callers SET revoked=1 WHERE id=? AND revoked=0 AND NOT EXISTS (SELECT 1 FROM grants WHERE caller=?)", id, id)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 0 {
				revoked = append(revoked, id)
			}
		}
		return nil
	})
	return revoked, err
}

func nativeSetupAffectedCallers(ctx context.Context, tx *sql.Tx, query string, args []any, affected map[string]bool) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		affected[id] = true
	}
	return rows.Err()
}
