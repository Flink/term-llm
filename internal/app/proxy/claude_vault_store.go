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
)

// Historical schema installed by migration 5 and dropped by migration 8.
const claudeVaultSchema = `CREATE TABLE claude_vault (
 id TEXT PRIMARY KEY, caller TEXT NOT NULL, route TEXT NOT NULL, authority TEXT NOT NULL,
 account TEXT NOT NULL, identity INTEGER NOT NULL, generation INTEGER NOT NULL,
 access_hash TEXT UNIQUE NOT NULL, refresh_hash TEXT UNIQUE NOT NULL,
 state TEXT NOT NULL, expires INTEGER NOT NULL, updated INTEGER NOT NULL, data BLOB NOT NULL
);
CREATE INDEX claude_vault_binding ON claude_vault(caller,route,authority,account,identity);`

var errClaudeVaultLogin = errors.New("native login required")

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
	Access       string `json:"access,omitempty"`
	Refresh      string `json:"refresh,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}
type claudeVaultRecord struct {
	state   string
	private claudeVaultPrivate
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

// Recheck the durable authorization and identity in the custody transaction,
// preventing a late issuer response from recreating a just-revoked mapping.
type claudeVaultQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func claudeVaultBindingValid(ctx context.Context, tx claudeVaultQuerier, scope claudeForwardScope) error {
	var data string
	err := tx.QueryRowContext(ctx, `SELECT a.data FROM accounts a, callers c JOIN grants g ON g.caller=c.id WHERE a.name=? AND a.kind=? AND c.id=? AND c.revoked=0 AND g.route=? AND g.authority=?`, scope.account, scope.nativeProvider().kind, scope.caller, scope.route, scope.authority).Scan(&data)
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
