package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"time"
)

const claudeSharedMode = "shared_vault"
const claudeSharedEnrollmentTTL = 15 * time.Minute

const claudeSharedSchema = `
CREATE TABLE claude_shared_enrollment (
 account TEXT PRIMARY KEY, caller TEXT NOT NULL, route TEXT NOT NULL,
 authority TEXT NOT NULL, expires INTEGER NOT NULL, reserved INTEGER NOT NULL DEFAULT 0
);`

func (a accountRecord) claudeShared() bool {
	return nativeProviderForKind(a.Kind) != nil && a.credentials.Mode == claudeSharedMode && a.claudeSharedID != ""
}

// The account namespace is random (and not reused if the account is removed).
// Aliases survive refresh and reconnect but cannot be used by a different
// CONNECT caller, even if both callers have access to the same Claude account.
func claudeSharedAlias(key [32]byte, a accountRecord, scope claudeForwardScope, prefix string) string {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(claudeVaultAAD("shared-alias:"+prefix+":"+a.claudeSharedID, claudeForwardScope{
		caller: scope.caller, route: scope.route, authority: scope.authority, account: a.Name,
	}))
	return prefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func claudeSharedSecretScope(a accountRecord) claudeForwardScope {
	return claudeForwardScope{account: a.Name, identity: a.identityVersion}
}

func claudeSharedSecret(key [32]byte, a accountRecord) (claudeVaultPrivate, error) {
	return claudeVaultOpen(key, "shared-account:"+a.claudeSharedID, claudeSharedSecretScope(a), a.claudeSharedCipher)
}

func sealNativeAccount(key [32]byte, a *accountRecord, reply claudeVaultReply, clientSecret string) error {
	cipher, err := claudeVaultSeal(key, "shared-account:"+a.claudeSharedID, claudeSharedSecretScope(*a), claudeVaultPrivate{
		Access: reply.AccessToken, Refresh: reply.RefreshToken, Scope: reply.Scope, ClientSecret: clientSecret,
	})
	if err != nil {
		return err
	}
	now := time.Now()
	a.claudeSharedCipher = cipher
	a.State, a.ExpiresAt, a.Updated = "ready", now.Unix()+reply.ExpiresIn, now.UnixMilli()
	return nil
}

func claudeSharedEnrollmentActive(ctx context.Context, q claudeVaultQuerier, scope claudeForwardScope) bool {
	var found int
	return q.QueryRowContext(ctx, `SELECT 1 FROM claude_shared_enrollment WHERE account=? AND caller=? AND route=? AND authority=? AND expires>? AND reserved=0`, scope.account, scope.caller, scope.route, scope.authority, time.Now().Unix()).Scan(&found) == nil
}

func (s *store) reserveClaudeSharedEnrollment(ctx context.Context, scope claudeForwardScope) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := claudeVaultBindingValid(ctx, tx, scope); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `UPDATE claude_shared_enrollment SET reserved=1 WHERE account=? AND caller=? AND route=? AND authority=? AND expires>? AND reserved=0`, scope.account, scope.caller, scope.route, scope.authority, time.Now().Unix())
		return claudeSharedRequireChange(r, err)
	})
}

func (s *store) finishClaudeSharedEnrollment(ctx context.Context, scope claudeForwardScope, before, after accountRecord) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := claudeVaultBindingValid(ctx, tx, scope); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM claude_shared_enrollment WHERE account=? AND caller=? AND route=? AND authority=? AND reserved=1 AND expires>?`, scope.account, scope.caller, scope.route, scope.authority, time.Now().Unix())
		if err := claudeSharedRequireChange(r, err); err != nil {
			return err
		}
		return updateClaudeSharedAccount(ctx, tx, before, after)
	})
}

func (s *store) changeClaudeSharedAccount(ctx context.Context, before, after accountRecord) error {
	return s.accountWrite(ctx, func(tx *sql.Tx) error { return updateClaudeSharedAccount(ctx, tx, before, after) })
}

func updateClaudeSharedAccount(ctx context.Context, tx *sql.Tx, before, after accountRecord) error {
	data, err := encodeAccount(after)
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE accounts SET state=?,generation=?,expires=?,updated=?,data=? WHERE name=? AND state=? AND generation=? AND json_extract(data,'$.claude_shared_id')=?`, after.State, after.Generation, after.ExpiresAt, after.Updated, string(data), before.Name, before.State, before.Generation, before.claudeSharedID)
	return claudeSharedRequireChange(r, err)
}

func claudeSharedRequireChange(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return errClaudeVaultLogin
	}
	return nil
}
