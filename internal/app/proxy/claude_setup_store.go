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
func (s *store) createClaudeSetup(ctx context.Context, next *snapshot, account *accountRecord, route, id, token string) error {
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
		if _, err := tx.ExecContext(ctx, "INSERT INTO callers(id,name,hash,created) VALUES(?,?,?,?)", id, "Claude Code", hashToken(token), time.Now().UnixMilli()); err != nil {
			return err
		}
		return writeGrants(tx, id, []string{route}, next)
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

func (s *store) clearClaudeSetup(ctx context.Context, next *snapshot, routes []string) ([]string, error) {
	var revoked []string
	err := s.accountWrite(ctx, func(tx *sql.Tx) error {
		// Include orphaned native reservations, but never infer ownership from a
		// client name. An unrelated empty client must not be revoked.
		affected := map[string]bool{}
		if err := claudeSetupAffectedCallers(ctx, tx, "SELECT DISTINCT caller FROM claude_vault", nil, affected); err != nil {
			return err
		}
		for _, route := range routes {
			if err := claudeSetupAffectedCallers(ctx, tx, "SELECT caller FROM grants WHERE route=?", []any{route}, affected); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM grants WHERE route=?", route); err != nil {
				return err
			}
		}
		// Delete every row, including spent receipts and in-flight code
		// reservations. Re-creating identical names/grants cannot revive them.
		if _, err := tx.ExecContext(ctx, "DELETE FROM claude_vault"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE kind='claude-code'"); err != nil {
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

func claudeSetupAffectedCallers(ctx context.Context, tx *sql.Tx, query string, args []any, affected map[string]bool) error {
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
