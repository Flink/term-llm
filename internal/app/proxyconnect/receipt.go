package proxyconnect

import (
	"encoding/hex"
	"encoding/json"
)

// These are exactly the keys owned by legacy version-2 native receipts.
var claudeNativeKeys = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES",
	"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy",
	"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy", "NODE_EXTRA_CA_CERTS",
	claudeBaseURLKey, claudeAuthTokenKey, "ANTHROPIC_API_KEY", "CLAUDE_CODE_API_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

func hexLength(s string, n int) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == n }

func nativeEnvDigest(raw json.RawMessage) string {
	if len(raw) == 0 {
		return tokenDigest("")
	}
	// Authenticate values, not insignificant escaping/whitespace in strings.
	if value, ok := jsonString(raw); ok {
		raw, _ = json.Marshal(value)
	}
	return tokenDigest(string(raw))
}

func validClaudeReceipt(record claudeReceiptFile) bool {
	if !hexLength(record.TokenSHA256, 32) {
		return false
	}
	if record.Version == 1 {
		return record.Legacy == nil && len(record.OwnedEnv) == 0
	}
	if record.Version != 2 || len(record.OwnedEnv) != len(claudeNativeKeys) {
		return false
	}
	for _, key := range claudeNativeKeys {
		owned, ok := record.OwnedEnv[key]
		if !ok || !hexLength(owned.WrittenSHA256, 32) {
			return false
		}
	}
	if record.Legacy != nil {
		return record.Legacy.Version == 1 && validClaudeReceipt(*record.Legacy) &&
			record.Legacy.GatewayURL == record.GatewayURL && record.Legacy.TokenSHA256 == record.TokenSHA256 && hexLength(record.UpgradeSHA256, 32)
	}
	return record.UpgradeSHA256 == ""
}

func (r claudeReceiptFile) ownsEnv(env *orderedObject) bool {
	if !validClaudeReceipt(r) {
		return false
	}
	if r.Version == 1 {
		gateway, isURL := jsonString(env.values[claudeBaseURLKey])
		token, isToken := jsonString(env.values[claudeAuthTokenKey])
		return isURL && isToken && gateway == r.GatewayURL && tokenDigest(token) == r.TokenSHA256
	}
	for key, owned := range r.OwnedEnv {
		if nativeEnvDigest(env.values[key]) != owned.WrittenSHA256 {
			return false
		}
	}
	token, ok := jsonString(env.values["CLAUDE_CODE_OAUTH_TOKEN"])
	return ok && tokenDigest(token) == r.TokenSHA256
}

func (r claudeReceiptFile) upgrading(current []byte, env *orderedObject) bool {
	return r.Version == 2 && r.Legacy != nil && tokenDigest(string(current)) == r.UpgradeSHA256 && r.Legacy.ownsEnv(env)
}
