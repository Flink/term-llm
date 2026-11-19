package proxy

import (
	"context"
	"database/sql"
)

// Apply only explicitly mentioned routes inside the transaction. In particular,
// never round-trip the client's other route names through the current snapshot:
// that would replace their saved authorities, widening stale or partial grants.
func (s *store) changeProviderGrants(ctx context.Context, id string, changes map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := editableCaller(tx, id); err != nil {
		return err
	}
	for route, authority := range changes {
		if err := changeProviderGrant(ctx, tx, id, route, authority); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func changeProviderGrant(ctx context.Context, tx *sql.Tx, id, route, authority string) error {
	var err error
	if authority == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM grants WHERE caller=? AND route=?", id, route)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO grants(caller,route,authority) VALUES(?,?,?)
			ON CONFLICT(caller,route) DO UPDATE SET authority=excluded.authority WHERE grants.authority!=excluded.authority`, id, route, authority)
	}
	if err != nil {
		return err
	}
	// An outstanding operator enrollment survives only an unchanged grant.
	_, err = tx.ExecContext(ctx, "DELETE FROM claude_shared_enrollment WHERE caller=? AND route=? AND (authority!=? OR ?='')", id, route, authority, authority)
	return err
}

// The caller holds s.mu, as for normal revocation. Retained sockets keep
// running; a revoked or superseded binding cannot admit or complete a socket.
func (s *Service) cancelChangedGrantSockets(id string, changes map[string]string) {
	s.sockets.Range(func(_, v any) bool {
		a := v.(*activeSocket)
		if authority, touched := changes[a.route]; a.caller == id && touched && (authority == "" || authority != a.authority) {
			a.cancel(errSocketRevoked)
		}
		return true
	})
}
