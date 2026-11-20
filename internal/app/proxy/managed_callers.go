package proxy

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	maxActiveCallers   = 1024
	maxRetainedCallers = 4096
	callerLimitError   = inputError("Client limit reached (1024); revoke unused clients or use a separate proxy state directory")
)

var (
	errCallerMissing = errors.New("Client not found")
	errCallerRevoked = errors.New("Client is already revoked")
	errCallerActive  = errors.New("Revoke the client before purging it")
)

type externalIDConflict struct{ clientID string }

func (e externalIDConflict) Error() string { return "External ID is held by an active client" }

type callerReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanCaller(row interface{ Scan(...any) error }) (Caller, error) {
	c := Caller{Grants: []string{}}
	err := row.Scan(&c.ID, &c.Name, &c.Created, &c.Revoked, &c.LastUsed, &c.CreatedBy, &c.ExternalID)
	return c, err
}

// The filter is an internal SQL fragment shared by the caller and grant scans.
func listCallers(ctx context.Context, db callerReader, where string, args ...any) ([]Caller, error) {
	rows, err := db.QueryContext(ctx, callerSelect+where+callerOrder, args...)
	if err != nil {
		return nil, err
	}
	out := []Caller{}
	byID := map[string]int{}
	for rows.Next() {
		c, err := scanCaller(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[c.ID] = len(out)
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(out) == 0 {
		return out, err
	}
	rows, err = db.QueryContext(ctx, "SELECT g.caller,g.route FROM grants g JOIN callers c ON c.id=g.caller"+where+" ORDER BY g.route", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, route string
		if err := rows.Scan(&id, &route); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].Grants = append(out[i].Grants, route)
		}
	}
	return out, rows.Err()
}

func (s *store) managedCallers(ctx context.Context, externalID string) ([]Caller, error) {
	if externalID != "" {
		return listCallers(ctx, s.db, " WHERE c.created_by!='' AND c.external_id=?", externalID)
	}
	return listCallers(ctx, s.db, " WHERE c.created_by!=''")
}

func managedCaller(ctx context.Context, db callerReader, id string) (Caller, error) {
	c, err := scanCaller(db.QueryRowContext(ctx, callerSelect+" WHERE c.id=? AND c.created_by!=''", id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, errCallerMissing
	}
	return c, err
}

func managedCallerWithGrants(ctx context.Context, db callerReader, id string) (Caller, error) {
	c, err := managedCaller(ctx, db, id)
	if err != nil {
		return c, err
	}
	c.Grants, err = selectStrings(ctx, db, "SELECT route FROM grants WHERE caller=? ORDER BY route", id)
	return c, err
}

func selectStrings(ctx context.Context, db callerReader, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *store) insertCaller(ctx context.Context, name string, grants []string, snap *snapshot, keyID, externalID string) (Caller, string, error) {
	c := Caller{ID: randomToken("c_")[:26], Name: name, Created: time.Now().UnixMilli(), Grants: grants, CreatedBy: keyID, ExternalID: externalID}
	token := randomToken("sk-tlp-")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, "", err
	}
	defer tx.Rollback()
	if externalID != "" {
		if err := checkExternalID(ctx, tx, externalID); err != nil {
			return c, "", err
		}
	}
	if err := makeCallerRoom(ctx, tx, callerLimitError); err != nil {
		return c, "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO callers(id,name,hash,created,created_by,external_id) VALUES(?,?,?,?,?,?)", c.ID, c.Name, hashToken(token), c.Created, c.CreatedBy, c.ExternalID); err != nil {
		return c, "", err
	}
	if err := writeGrants(tx, c.ID, grants, snap); err != nil {
		return c, "", err
	}
	if err := tx.Commit(); err != nil {
		return c, "", err
	}
	return c, token, nil
}

func checkExternalID(ctx context.Context, tx *sql.Tx, externalID string) error {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM callers WHERE external_id=? AND external_id!='' AND revoked=0 AND created_by!=''", externalID).Scan(&id)
	if err == nil {
		return externalIDConflict{id}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func makeCallerRoom(ctx context.Context, tx *sql.Tx, limitError inputError) error {
	var total, active int
	if err := tx.QueryRowContext(ctx, "SELECT count(*),count(*) FILTER (WHERE revoked=0) FROM callers").Scan(&total, &active); err != nil {
		return err
	}
	if active >= maxActiveCallers {
		return limitError
	}
	if total < maxRetainedCallers {
		return nil
	}
	n := total - maxRetainedCallers + 1
	ids, err := selectStrings(ctx, tx, "SELECT id FROM callers WHERE revoked!=0 ORDER BY created,id LIMIT ?", n)
	if err != nil {
		return err
	}
	if len(ids) != n {
		return limitError
	}
	for _, id := range ids {
		if err := purgeCallerTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) regrantManagedCaller(ctx context.Context, id string, grants []string, snap *snapshot) (Caller, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Caller{}, err
	}
	defer tx.Rollback()
	c, err := managedCaller(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if c.Revoked {
		return c, errCallerRevoked
	}
	if err := replaceGrants(tx, id, grants, snap); err != nil {
		return c, err
	}
	c.Grants = grants
	return c, tx.Commit()
}

func (s *store) revokeManagedCaller(ctx context.Context, id string) (Caller, error) {
	var c Caller
	err := s.accountWrite(ctx, func(tx *sql.Tx) error {
		var err error
		c, err = managedCaller(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.Revoked {
			return errCallerRevoked
		}
		return revokeCallerTx(ctx, tx, id)
	})
	return c, err
}

// purgeCaller is the machine operation; only provisioned, revoked clients are
// eligible. Automatic retention can purge revoked callers of either origin.
func (s *store) purgeCaller(ctx context.Context, id string) (Caller, error) {
	var c Caller
	err := s.accountWrite(ctx, func(tx *sql.Tx) error {
		var err error
		c, err = managedCaller(ctx, tx, id)
		if err != nil {
			return err
		}
		if !c.Revoked {
			return errCallerActive
		}
		return purgeCallerTx(ctx, tx, id)
	})
	return c, err
}

// purgeRevokedCallers deletes one revoked client, or all of them if id is "".
func (s *store) purgeRevokedCallers(ctx context.Context, id string) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		ids, err := selectStrings(ctx, tx, "SELECT id FROM callers WHERE revoked!=0 AND (?='' OR id=?)", id, id)
		if err != nil {
			return fmt.Errorf("list revoked clients: %w", err)
		}
		if id != "" && len(ids) == 0 {
			if ids, err = selectStrings(ctx, tx, "SELECT id FROM callers WHERE id=?", id); err == nil && len(ids) == 0 {
				return errCallerMissing
			}
			return cmp.Or(err, errCallerActive)
		}
		for _, id := range ids {
			if err := purgeCallerTx(ctx, tx, id); err != nil {
				return fmt.Errorf("purge revoked client: %w", err)
			}
		}
		return nil
	})
}

// No request or money history is joined or removed. Its independent snapshots
// preserve attribution, including for work still running when a row is purged.
func purgeCallerTx(ctx context.Context, tx *sql.Tx, id string) error {
	for _, stmt := range []string{
		"DELETE FROM grants WHERE caller=?",
		"DELETE FROM claude_shared_enrollment WHERE caller=?",
		"DELETE FROM callers WHERE id=? AND revoked!=0",
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return err
		}
	}
	return nil
}
