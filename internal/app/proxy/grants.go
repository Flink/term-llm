package proxy

import (
	"context"
	"database/sql"
)

type inputError string

func (e inputError) Error() string { return string(e) }

// writeGrants is shared by create and edit; it never reissues a caller token.
func writeGrants(tx *sql.Tx, id string, grants []string, snap *snapshot) error {
	if len(grants) > 128 {
		return inputError("at most 128 route grants per client")
	}
	seen := map[string]bool{}
	for _, name := range grants {
		if !slug.MatchString(name) || seen[name] {
			return inputError("route grants must contain unique valid route names")
		}
		seen[name] = true
		var authority string
		for _, r := range snap.routes {
			if r.Name == name {
				authority = r.authority
				break
			}
		}
		if authority == "" {
			return inputError("route grant is not in the active configuration; refresh before saving")
		}
		if _, err := tx.Exec("INSERT INTO grants(caller,route,authority) VALUES(?,?,?)", id, name, authority); err != nil {
			return err
		}
	}
	return nil
}
func (s *store) updateGrants(id string, grants []string, snap *snapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked bool
	if err := tx.QueryRow("SELECT revoked FROM callers WHERE id=?", id).Scan(&revoked); err == sql.ErrNoRows {
		return inputError("client no longer exists; refresh before editing grants")
	} else if err != nil {
		return err
	}
	if revoked {
		return inputError("revoked clients cannot receive new grants; create a new client")
	}
	if err := replaceGrants(tx, id, grants, snap); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceGrants(tx *sql.Tx, id string, grants []string, snap *snapshot) error {
	if _, err := tx.Exec("DELETE FROM grants WHERE caller=?", id); err != nil {
		return err
	}
	if err := writeGrants(tx, id, grants, snap); err != nil {
		return err
	}
	// Revoking and later regranting an identical route must not resurrect a
	// native login. Retained grants keep their existing isolated sessions.
	_, err := tx.Exec("DELETE FROM claude_vault WHERE caller=? AND NOT EXISTS (SELECT 1 FROM grants g WHERE g.caller=claude_vault.caller AND g.route=claude_vault.route AND g.authority=claude_vault.authority)", id)
	return err
}

// revokeCaller clears the caller's native custody in the same durable commit as
// revocation. A late token exchange cannot re-create it after this fence.
func (s *store) revokeCaller(ctx context.Context, id string) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		return revokeCallerTx(ctx, tx, id)
	})
}

func revokeCallerTx(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE callers SET revoked=1 WHERE id=?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM claude_vault WHERE caller=?", id)
	return err
}
