package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/deviceflow"
)

const accountSchema = `CREATE TABLE accounts (name TEXT PRIMARY KEY, kind TEXT NOT NULL, state TEXT NOT NULL, generation INTEGER NOT NULL, expires INTEGER NOT NULL, updated INTEGER NOT NULL, data TEXT NOT NULL);`

// AccountInfo is the entire public account representation. Credentials, identity
// IDs, fixture endpoints and bootstrap paths must never be exported.
type AccountInfo struct {
	DeviceReconnect deviceflow.Reconnect `json:"device_reconnect,omitempty"`
	Name            string               `json:"name"`
	Kind            string               `json:"kind"`
	AuthMode        string               `json:"auth_mode,omitempty"`
	State           string               `json:"state"`
	Generation      int64                `json:"generation"`
	ExpiresAt       int64                `json:"expires_at,omitempty"`
	Updated         int64                `json:"updated"`
}

type accountRecord struct {
	AccountInfo
	credentials        accountCredentials
	operator           accountOperator
	endpoint           string
	identityVersion    int64
	identityHash       string // provider-scoped digest retained across sign-out
	refresh            accountRefreshState
	claudeSharedID     string
	claudeSharedCipher []byte
}
type accountPrivate struct {
	Credentials         accountCredentials  `json:"credentials"`
	Operator            accountOperator     `json:"operator,omitempty"`
	Endpoint            string              `json:"endpoint,omitempty"`
	Refresh             accountRefreshState `json:"refresh,omitempty"`
	IdentityVersion     int64               `json:"identity_version,omitempty"`
	IdentityHash        string              `json:"device_identity_hash,omitempty"`
	ChatGPTIdentityHash string              `json:"chatgpt_identity_hash,omitempty"`
	ClaudeSharedID      string              `json:"claude_shared_id,omitempty"`
	ClaudeSharedCipher  []byte              `json:"claude_shared_cipher,omitempty"`
}

func (s *store) account(name string) (accountRecord, error) {
	return scanAccount(s.db.QueryRow("SELECT "+accountColumns+" FROM accounts WHERE name=?", name))
}

const accountColumns = "name,kind,state,generation,expires,updated,data"

func scanAccount(row interface{ Scan(...any) error }) (accountRecord, error) {
	var a accountRecord
	var data string
	err := row.Scan(&a.Name, &a.Kind, &a.State, &a.Generation, &a.ExpiresAt, &a.Updated, &data)
	if err != nil {
		return a, err
	}
	var private accountPrivate
	if err = json.Unmarshal([]byte(data), &private); err != nil {
		return a, err
	}
	a.credentials, a.operator, a.endpoint = private.Credentials, private.Operator, private.Endpoint
	a.refresh = private.Refresh
	a.identityVersion = private.IdentityVersion
	a.identityHash = private.IdentityHash
	if a.Kind == "chatgpt" {
		a.identityHash = private.ChatGPTIdentityHash
	}
	a.claudeSharedID, a.claudeSharedCipher = private.ClaudeSharedID, private.ClaudeSharedCipher
	a.AccountInfo = a.publicInfo()
	return a, nil
}
func (s *store) accountInfos() ([]AccountInfo, error) {
	rows, err := s.db.Query("SELECT " + accountColumns + " FROM accounts ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountInfo{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a.AccountInfo)
	}
	return out, rows.Err()
}

// accountWrite uses FULL only for custody commits. The single SQLite connection
// prevents unrelated writes from seeing the temporary setting. Never hold it
// during an issuer call. Restore NORMAL for the existing generic request ledger.
func (s *store) accountWrite(ctx context.Context, fn func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	defer func() {
		reset, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(reset, "PRAGMA synchronous=NORMAL")
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		// database/sql marks a Tx done even when SQLite rejects COMMIT (for example
		// a deferred constraint). Explicitly end any remaining SQLite transaction
		// before returning this connection to the pool or restoring its PRAGMA.
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = conn.ExecContext(cleanup, "ROLLBACK")
		cancel()
		return err
	}
	return nil
}
func encodeAccount(a accountRecord) ([]byte, error) {
	private := accountPrivate{Credentials: a.credentials, Operator: a.operator, Endpoint: a.endpoint, Refresh: a.refresh, IdentityVersion: a.identityVersion, ClaudeSharedID: a.claudeSharedID, ClaudeSharedCipher: a.claudeSharedCipher}
	if a.Kind == "chatgpt" {
		private.ChatGPTIdentityHash = a.identityHash
	} else {
		private.IdentityHash = a.identityHash
	}
	return json.Marshal(private)
}
func insertAccount(ctx context.Context, tx *sql.Tx, a accountRecord) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&n); err != nil {
		return err
	}
	if n >= 128 {
		return inputError("at most 128 accounts")
	}
	data, err := encodeAccount(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO accounts(name,kind,state,generation,expires,updated,data) VALUES(?,?,?,?,?,?,?)", a.Name, a.Kind, a.State, a.Generation, a.ExpiresAt, a.Updated, string(data))
	return err
}

func (s *store) replaceAccount(ctx context.Context, a accountRecord, expected int64) error {
	if expected == 0 {
		return s.accountWrite(ctx, func(tx *sql.Tx) error { return insertAccount(ctx, tx, a) })
	}
	data, err := encodeAccount(a)
	if err != nil {
		return err
	}
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		result, e := tx.Exec("UPDATE accounts SET state=?,generation=?,expires=?,updated=?,data=? WHERE name=? AND generation=? AND kind=?", a.State, a.Generation, a.ExpiresAt, a.Updated, string(data), a.Name, expected, a.Kind)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("account changed")
		}
		if a.claudeShared() && a.State == "needs_login" {
			_, e = tx.ExecContext(ctx, "DELETE FROM claude_shared_enrollment WHERE account=?", a.Name)
		}
		return e
	})
}
func (s *store) fenceAccount(ctx context.Context, name string, generation int64) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE accounts SET state='refreshing',updated=? WHERE name=? AND generation=? AND state='ready'", time.Now().UnixMilli(), name, generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("account not ready")
		}
		return nil
	})
}
func (s *store) interruptAccounts(ctx context.Context) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE accounts SET state=CASE WHEN kind='copilot' THEN 'ready' ELSE 'needs_login' END WHERE state='refreshing'")
		return err
	})
}

func (a accountRecord) publicInfo() AccountInfo {
	info := a.AccountInfo
	info.AuthMode = accountAuthMode(a.Kind, a.credentials)
	info.DeviceReconnect = deviceflow.ReconnectEligibility(a.Kind, a.operator.Issuer != "" || a.operator.ClientID != "" || len(a.operator.Origins) != 0, a.credentials.AccountID != "" || a.identityHash != "")
	return info
}
