package proxy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Migration 5 installs this schema without rewriting shared account custody.
// Only token hashes and binding metadata are public to SQLite; real
// tokens and retry receipts are authenticated ciphertext under the private CA.
const claudeVaultSchema = `CREATE TABLE claude_vault (
 id TEXT PRIMARY KEY, caller TEXT NOT NULL, route TEXT NOT NULL, authority TEXT NOT NULL,
 account TEXT NOT NULL, identity INTEGER NOT NULL, generation INTEGER NOT NULL,
 access_hash TEXT UNIQUE NOT NULL, refresh_hash TEXT UNIQUE NOT NULL,
 state TEXT NOT NULL, expires INTEGER NOT NULL, updated INTEGER NOT NULL, data BLOB NOT NULL
);
CREATE INDEX claude_vault_binding ON claude_vault(caller,route,authority,account,identity);`

var errClaudeVaultLogin = errors.New("native login required")

const claudeVaultMaxRows = 4096

// No raw upstream response survives this allowlist, including unknown nested
// credential fields, error messages, HTTP headers, or Console API key grants.
type claudeVaultReply struct {
	AccessToken  string                   `json:"access_token"`
	RefreshToken string                   `json:"refresh_token"`
	TokenType    string                   `json:"token_type"`
	ExpiresIn    int64                    `json:"expires_in"`
	Scope        string                   `json:"scope"`
	Account      *claudeVaultAccount      `json:"account,omitempty"`
	Organization *claudeVaultOrganization `json:"organization,omitempty"`
}
type claudeVaultPrivate struct {
	Access  string            `json:"access,omitempty"`
	Refresh string            `json:"refresh,omitempty"`
	Scope   string            `json:"scope,omitempty"`
	Result  *claudeVaultReply `json:"result,omitempty"`
}
type claudeVaultRecord struct {
	id                  string
	generation, expires int64
	state               string
	private             claudeVaultPrivate
}

func claudeVaultSeal(key [32]byte, id string, scope claudeForwardScope, value claudeVaultPrivate) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, claudeVaultAAD(id, scope)), nil
}
func claudeVaultOpen(key [32]byte, id string, scope claudeForwardScope, data []byte) (claudeVaultPrivate, error) {
	var value claudeVaultPrivate
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return value, errClaudeVaultLogin
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < gcm.NonceSize() {
		return value, errClaudeVaultLogin
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], claudeVaultAAD(id, scope))
	if err != nil || json.Unmarshal(plain, &value) != nil {
		return value, errClaudeVaultLogin
	}
	return value, nil
}

func (s *store) claudeVaultLookup(ctx context.Context, key [32]byte, scope claudeForwardScope, token string, refresh bool) (claudeVaultRecord, error) {
	var r claudeVaultRecord
	prefix := "sk-ant-oat01-tlv2-"
	if refresh {
		prefix = "sk-ant-ort01-tlv2-"
	}
	if len(token) > 16<<10 || !strings.HasPrefix(token, prefix) {
		return r, errClaudeVaultLogin
	}
	column := "access_hash"
	if refresh {
		column = "refresh_hash"
	}
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT id,generation,expires,state,data FROM claude_vault WHERE "+column+"=? AND caller=? AND route=? AND authority=? AND account=? AND identity=?", hashToken(token), scope.caller, scope.route, scope.authority, scope.account, scope.identity).Scan(&r.id, &r.generation, &r.expires, &r.state, &data)
	if err != nil {
		return r, errClaudeVaultLogin
	}
	r.private, err = claudeVaultOpen(key, r.id, scope, data)
	if err != nil {
		return r, errClaudeVaultLogin
	}
	return r, nil
}
func (s *store) claudeVaultAccess(ctx context.Context, key [32]byte, scope claudeForwardScope) (string, error) {
	r, err := s.claudeVaultLookup(ctx, key, scope, scope.access, false)
	if err != nil || r.state != "ready" || r.expires <= time.Now().Unix() || r.private.Access == "" {
		return "", errClaudeVaultLogin
	}
	if err := claudeVaultBindingValid(ctx, s.db, scope); err != nil {
		return "", err
	}
	return r.private.Access, nil
}

// The before-send state transition is a FULL synchronous commit. No network
// call may precede it. A crash, cancellation, malformed result or ambiguous
// issuer failure leaves 'refreshing' permanently non-replayable, even on restart.
func (s *store) claudeVaultFence(ctx context.Context, id string, scope claudeForwardScope) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := claudeVaultBindingValid(ctx, tx, scope); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE claude_vault SET state='refreshing',updated=? WHERE id=? AND state='ready'", time.Now().Unix(), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return errClaudeVaultLogin
		}
		return nil
	})
}
func (s *store) claudeVaultSave(ctx context.Context, key [32]byte, scope claudeForwardScope, upstream claudeVaultReply, previous *claudeVaultRecord) (claudeVaultReply, error) {
	result := upstream
	result.AccessToken = randomToken("sk-ant-oat01-tlv2-")
	result.RefreshToken = randomToken("sk-ant-ort01-tlv2-")
	id := randomToken("cv_")
	codeExchange := previous != nil && previous.state == "exchanging"
	if codeExchange {
		id = previous.id
	}
	data, err := claudeVaultSeal(key, id, scope, claudeVaultPrivate{Access: upstream.AccessToken, Refresh: upstream.RefreshToken, Scope: upstream.Scope})
	if err != nil {
		return claudeVaultReply{}, err
	}
	var receipt []byte
	generation := int64(1)
	if previous != nil && !codeExchange {
		generation = previous.generation + 1
		receipt, err = claudeVaultSeal(key, previous.id, scope, claudeVaultPrivate{Result: &result})
		if err != nil {
			return claudeVaultReply{}, err
		}
	}
	err = s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := claudeVaultBindingValid(ctx, tx, scope); err != nil {
			return err
		}
		if err := claudeVaultMakeRoom(ctx, tx, scope, previous == nil, !codeExchange); err != nil {
			return err
		}
		if codeExchange {
			changed, e := tx.ExecContext(ctx, "UPDATE claude_vault SET generation=1,access_hash=?,refresh_hash=?,state='ready',expires=?,updated=?,data=? WHERE id=? AND state='exchanging'", hashToken(result.AccessToken), hashToken(result.RefreshToken), time.Now().Unix()+result.ExpiresIn, time.Now().Unix(), data, id)
			if e != nil {
				return e
			}
			n, e := changed.RowsAffected()
			if e != nil || n != 1 {
				return errClaudeVaultLogin
			}
			return nil
		}
		if previous != nil {
			changed, e := tx.ExecContext(ctx, "UPDATE claude_vault SET state='spent',data=?,updated=? WHERE id=? AND state='refreshing'", receipt, time.Now().Unix(), previous.id)
			if e != nil {
				return e
			}
			n, e := changed.RowsAffected()
			if e != nil || n != 1 {
				return errClaudeVaultLogin
			}
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO claude_vault(id,caller,route,authority,account,identity,generation,access_hash,refresh_hash,state,expires,updated,data) VALUES(?,?,?,?,?,?,?,?,?,'ready',?,?,?)", id, scope.caller, scope.route, scope.authority, scope.account, scope.identity, generation, hashToken(result.AccessToken), hashToken(result.RefreshToken), time.Now().Unix()+result.ExpiresIn, time.Now().Unix(), data)
		return e
	})
	if err != nil {
		return claudeVaultReply{}, err
	}
	return result, nil
}

// Recheck the durable authorization and identity in the custody transaction,
// preventing a late issuer response from recreating a just-revoked mapping.
type claudeVaultQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func claudeVaultBindingValid(ctx context.Context, tx claudeVaultQuerier, scope claudeForwardScope) error {
	var data string
	err := tx.QueryRowContext(ctx, `SELECT a.data FROM accounts a, callers c JOIN grants g ON g.caller=c.id WHERE a.name=? AND a.kind='claude-code' AND c.id=? AND c.revoked=0 AND g.route=? AND g.authority=?`, scope.account, scope.caller, scope.route, scope.authority).Scan(&data)
	if err != nil {
		return errClaudeVaultLogin
	}
	var account accountPrivate
	if json.Unmarshal([]byte(data), &account) != nil || account.IdentityVersion != scope.identity {
		return errClaudeVaultLogin
	}
	return nil
}

// Authenticate the principal/route/account binding as well as ciphertext. The
// placeholder is deliberately not included: refresh lookups and API lookups
// have different placeholder values but address the same generation.
func claudeVaultAAD(id string, scope claudeForwardScope) []byte {
	return []byte(strings.Join([]string{"claude-vault-v2", id, scope.caller, scope.route, scope.authority, scope.account, strconv.FormatInt(scope.identity, 10)}, "\x00"))
}

// Retry receipts refer only to their immediate successor. Returning an expired,
// spent or uncertain successor would resurrect dead credentials in the client.
func (s *store) claudeVaultRetry(ctx context.Context, key [32]byte, scope claudeForwardScope, previous claudeVaultRecord) (claudeVaultReply, error) {
	if previous.state != "spent" || previous.private.Result == nil {
		return claudeVaultReply{}, errClaudeVaultLogin
	}
	reply := *previous.private.Result
	next, err := s.claudeVaultLookup(ctx, key, scope, reply.AccessToken, false)
	if err != nil || next.state != "ready" || next.generation != previous.generation+1 {
		return claudeVaultReply{}, errClaudeVaultLogin
	}
	remaining := next.expires - time.Now().Unix()
	if remaining <= 0 || claudeVaultBindingValid(ctx, s.db, scope) != nil {
		return claudeVaultReply{}, errClaudeVaultLogin
	}
	// Never increase a cached lifetime, even if a wall clock adjustment occurs.
	if remaining < reply.ExpiresIn {
		reply.ExpiresIn = remaining
	}
	return reply, nil
}

// Install a durable reservation before a code leaves the host. Existing grant
// and identity mutation hooks delete it, so even revoke+identical-regrant while
// the issuer is in flight cannot resurrect a revoked authorization attempt.
func (s *store) claudeVaultBeginCode(ctx context.Context, key [32]byte, scope claudeForwardScope) (claudeVaultRecord, error) {
	record := claudeVaultRecord{id: randomToken("cv_"), state: "exchanging"}
	data, err := claudeVaultSeal(key, record.id, scope, claudeVaultPrivate{})
	if err != nil {
		return claudeVaultRecord{}, err
	}
	err = s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := claudeVaultBindingValid(ctx, tx, scope); err != nil {
			return err
		}
		if err := claudeVaultMakeRoom(ctx, tx, scope, true, true); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO claude_vault(id,caller,route,authority,account,identity,generation,access_hash,refresh_hash,state,expires,updated,data) VALUES(?,?,?,?,?,?,0,?,?,'exchanging',?,?,?)", record.id, scope.caller, scope.route, scope.authority, scope.account, scope.identity, hashToken(randomToken("pending_access_")), hashToken(randomToken("pending_refresh_")), time.Now().Add(claudeVaultOAuthTimeout).Unix(), time.Now().Unix(), data)
		return err
	})
	return record, err
}

func claudeVaultMakeRoom(ctx context.Context, tx *sql.Tx, scope claudeForwardScope, active, row bool) error {
	// Never evict a live refresh token merely because its access token expired.
	// Spent receipts expire in 24h; abandoned code reservations in five minutes.
	if _, err := tx.ExecContext(ctx, "DELETE FROM claude_vault WHERE (state='spent' AND updated<?) OR (state='exchanging' AND updated<?)", time.Now().Add(-24*time.Hour).Unix(), time.Now().Add(-5*time.Minute).Unix()); err != nil {
		return err
	}
	var count int
	if row {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM claude_vault").Scan(&count); err != nil {
			return err
		}
		if count >= claudeVaultMaxRows {
			return errClaudeVaultLogin
		}
	}
	if active {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM claude_vault WHERE caller=? AND state!='spent'", scope.caller).Scan(&count); err != nil {
			return err
		}
		if count >= 64 {
			return errClaudeVaultLogin
		}
	}
	return nil
}
