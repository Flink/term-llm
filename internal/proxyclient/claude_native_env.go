package proxyclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	claudeNativeCAFile      = "claude-proxy-ca-v2.pem"
	claudeNativeSettings    = "settings.json"
	claudeNativeLoginMethod = "claudeai"
	claudeNativeSettingsMax = 1 << 20
)

// claudeNativeEnvKeys are replaced or removed in the returned environment.
// Everything else, including unrelated provider configuration and Claude Code
// isolation flags, is preserved.
var claudeNativeEnvKeys = map[string]bool{
	"ANTHROPIC_BASE_URL":                     true,
	"ANTHROPIC_AUTH_TOKEN":                   true,
	"ANTHROPIC_API_KEY":                      true,
	"ANTHROPIC_CONFIG_DIR":                   true,
	"ANTHROPIC_PROFILE":                      true,
	"ANTHROPIC_UNIX_SOCKET":                  true,
	"ANTHROPIC_FEDERATION_RULE_ID":           true,
	"ANTHROPIC_ORGANIZATION_ID":              true,
	"ANTHROPIC_IDENTITY_TOKEN":               true,
	"ANTHROPIC_IDENTITY_TOKEN_FILE":          true,
	"ANTHROPIC_SERVICE_ACCOUNT_ID":           true,
	"ANTHROPIC_WORKSPACE_ID":                 true,
	"ANTHROPIC_GOOGLE_CLOUD_BASE_URL":        true,
	"ANTHROPIC_GOOGLE_CLOUD_LOCATION":        true,
	"ANTHROPIC_GOOGLE_CLOUD_PROJECT":         true,
	"ANTHROPIC_GOOGLE_CLOUD_WORKSPACE_ID":    true,
	"ANTHROPIC_AWS_API_KEY":                  true,
	"ANTHROPIC_AWS_BASE_URL":                 true,
	"ANTHROPIC_AWS_WORKSPACE_ID":             true,
	"CLAUDE_CODE_API_BASE_URL":               true,
	claudeOAuthTokenEnv:                      true,
	"CLAUDE_CODE_OAUTH_TOKEN_FILE":           true,
	claudeOAuthFDEnv:                         true,
	"CLAUDE_CODE_OAUTH_REFRESH_TOKEN":        true,
	claudeOAuthScopesEnv:                     true,
	"CLAUDE_CODE_OAUTH_CLIENT_ID":            true,
	"CLAUDE_CODE_CUSTOM_OAUTH_URL":           true,
	"CLAUDE_CODE_SESSIONS_OAUTH_TOKEN":       true,
	"CLAUDE_CODE_SESSION_ACCESS_TOKEN":       true,
	"CLAUDE_CODE_API_KEY_HELPER_FD":          true,
	"CLAUDE_CODE_HOST_CREDS_FILE":            true,
	"CLAUDE_CODE_HOST_AUTH_ENV_VAR":          true,
	"CCR_OAUTH_TOKEN_FILE":                   true,
	"ANTHROPIC_CUSTOM_HEADERS":               true,
	"CLAUDE_CODE_USE_BEDROCK":                true,
	"CLAUDE_CODE_USE_VERTEX":                 true,
	"CLAUDE_CODE_USE_FOUNDRY":                true,
	"CLAUDE_CODE_USE_ANTHROPIC_AWS":          true,
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD": true,
	"CLAUDE_CODE_USE_MANTLE":                 true,
	"ANTHROPIC_BEDROCK_BASE_URL":             true,
	"ANTHROPIC_BEDROCK_MANTLE_BASE_URL":      true,
	"ANTHROPIC_BEDROCK_REGION_PREFIX":        true,
	"ANTHROPIC_BEDROCK_SERVICE_TIER":         true,
	"ANTHROPIC_VERTEX_BASE_URL":              true,
	"ANTHROPIC_VERTEX_PROJECT_ID":            true,
	"ANTHROPIC_FOUNDRY_BASE_URL":             true,
	"ANTHROPIC_FOUNDRY_API_KEY":              true,
	"ANTHROPIC_FOUNDRY_AUTH_TOKEN":           true,
	"ANTHROPIC_FOUNDRY_RESOURCE":             true,
	"AWS_BEARER_TOKEN_BEDROCK":               true,
	"AWS_ACCESS_KEY_ID":                      true,
	"AWS_SECRET_ACCESS_KEY":                  true,
	"AWS_SESSION_TOKEN":                      true,
	"HTTP_PROXY":                             true,
	"HTTPS_PROXY":                            true,
	"ALL_PROXY":                              true,
	"NO_PROXY":                               true,
	"http_proxy":                             true,
	"https_proxy":                            true,
	"all_proxy":                              true,
	"no_proxy":                               true,
	"NODE_EXTRA_CA_CERTS":                    true,
	"NODE_TLS_REJECT_UNAUTHORIZED":           true,
	"CLAUDE_CONFIG_DIR":                      true,
	"TERM_LLM_CLAUDE_PROXY_ROOT":             true,
}

// claudeNativeEnvironment builds the child-only environment. The proxy URL
// embeds the route as user name and the scoped token as password, which native
// clients turn into Proxy-Authorization: Basic. Loopback stays outside the
// proxy so native MCP and local tools keep working, while inherited exclusions
// that could match either Anthropic host are dropped. The CA is trusted only
// through NODE_EXTRA_CA_CERTS. Any shared placeholder is attached separately
// to the prepared command, not included in this environment.
func claudeNativeEnvironment(env []string, bootstrap *nativeBootstrap, token, caPath, stateDir string) ([]string, error) {
	proxyEndpoint := bootstrap.connectURL(token)
	noProxy := ClaudeNativeNoProxy(env)

	forced := []struct{ key, value string }{
		// Claude applies settings.json env after exec. Mark routing as host-managed
		// so project and profile settings cannot replace the scoped proxy.
		{"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "1"},
		{"HTTPS_PROXY", proxyEndpoint},
		{"HTTP_PROXY", proxyEndpoint},
		{"NO_PROXY", noProxy},
		{"no_proxy", noProxy},
		{"NODE_EXTRA_CA_CERTS", caPath},
		{"ANTHROPIC_CUSTOM_HEADERS", "X-Term-LLM-Proxy-App: term-llm"},
		{"CLAUDE_CONFIG_DIR", stateDir},
		{"ANTHROPIC_CONFIG_DIR", stateDir},
	}
	replace := make(map[string]string, len(forced))
	for _, entry := range forced {
		replace[entry.key] = entry.value
	}
	out := make([]string, 0, len(env)+len(forced))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		// Simple mode disables OAuth, including descriptor-supplied access.
		if key == "CLAUDE_CODE_SIMPLE" && bootstrap.claudeAccess.placeholder != "" {
			continue
		}
		if _, replaced := replace[key]; replaced || claudeNativeEnvKeys[key] {
			continue
		}
		out = append(out, entry)
	}
	for _, entry := range forced {
		out = append(out, entry.key+"="+entry.value)
	}
	return out, nil
}

// ClaudeNativeNoProxy returns the canonical NO_PROXY value for a native Claude
// child. Loopback is always present so local MCP servers and tools keep working,
// and safe entries from the supplied environment's NO_PROXY/no_proxy are
// preserved. Wildcards, the API and platform hosts, their parent and child
// domains, and port-qualified variants are dropped; no returned entry can match
// api.anthropic.com or platform.claude.com. The result is idempotent for
// already-canonical values.
func ClaudeNativeNoProxy(env []string) string {
	entries := []string{"localhost", "127.0.0.1", "::1"}
	seen := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		for _, raw := range strings.Split(claudeNativeEnvValue(env, key), ",") {
			entry := strings.TrimSpace(raw)
			if entry == "" || claudeNativeNoProxyCouldReachAPI(entry) {
				continue
			}
			normalized := strings.ToLower(entry)
			if !seen[normalized] {
				seen[normalized] = true
				entries = append(entries, entry)
			}
		}
	}
	return strings.Join(entries, ",")
}

func claudeNativeEnvValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

// claudeNativeNoProxyCouldReachAPI is deliberately narrow: only plain DNS
// names and IP literals with an optional port are considered. Any slash (CIDR
// or URL), wildcard, whitespace/control, non-ASCII or other punctuation is
// rejected because another proxy implementation might interpret it differently.
func claudeNativeNoProxyCouldReachAPI(entry string) bool {
	value := strings.ToLower(strings.TrimSpace(entry))
	if value == "" {
		return true
	}
	for _, r := range value {
		if r > 127 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == ':' || r == '[' || r == ']') {
			return true
		}
	}
	for _, host := range claudeNativeHosts {
		if strings.Contains(value, host) {
			return true
		}
	}
	host := value
	if split, _, err := net.SplitHostPort(value); err == nil {
		host = split
	} else if strings.Count(value, ":") == 1 {
		host = value[:strings.LastIndex(value, ":")]
	}
	host = strings.Trim(strings.Trim(host, "[]"), ".")
	if host == "" {
		return true
	}
	for _, target := range claudeNativeHosts {
		if host == target || strings.HasSuffix(target, "."+host) || strings.HasSuffix(host, "."+target) {
			return true
		}
	}
	return false
}

func prepareClaudeNativeState(stateDir string, caPEM []byte) (string, error) {
	caPath, err := claudeNativePolicy.prepareCA(stateDir, claudeNativeCAFile, caPEM)
	if err != nil {
		return "", err
	}
	if err := ensureClaudeNativeLoginSettings(filepath.Clean(stateDir)); err != nil {
		return "", err
	}
	return caPath, nil
}

func (p nativePolicy) prepareCA(stateDir, filename string, caPEM []byte) (string, error) {
	dir := filepath.Clean(stateDir)
	if dir == "." || dir == string(filepath.Separator) {
		return "", p.errorf("state must be a private directory")
	}
	if err := fileprivacy.MkdirAll(dir); err != nil {
		return "", p.errorf("could not create the private state directory")
	}
	if err := checkNativePrivateDir(dir); err != nil {
		return "", p.errorf("state must be a private non-symlink directory")
	}
	path := filepath.Join(dir, filename)
	return path, p.writeCAFile(path, caPEM)
}

func checkNativePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || fileprivacy.CheckDir(info) != nil {
		return errors.New("not a private non-symlink directory")
	}
	return nil
}

func (p nativePolicy) writeCAFile(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > nativeCAMaxBytes {
			return p.errorf("CA must be a private regular file")
		}
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return p.errorf("CA file could not be read")
		}
		if bytes.Equal(current, data) && fileprivacy.CheckFile(info) == nil {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return p.errorf("CA file could not be inspected")
	}
	if err := fileprivacy.WriteFileAtomic(path, data); err != nil {
		return p.errorf("CA file could not be written")
	}
	return nil
}

// ensureClaudeNativeLoginSettings pins the isolated profile to claude.ai
// subscription login. Console's keyless sign-in is stored outside the
// configuration directory, so it must not be offered. The file is term-owned
// but is never overwritten blindly: an existing forceLoginMethod for another
// method is a conflict, and an unreadable or malformed file fails closed.
func ensureClaudeNativeLoginSettings(dir string) error {
	path := filepath.Join(dir, claudeNativeSettings)
	settings := map[string]json.RawMessage{}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Claude proxy settings must be a private regular file")
		}
		if info.Size() > claudeNativeSettingsMax {
			return errors.New("Claude proxy settings are too large; refusing to modify them")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return errors.New("could not read the Claude proxy settings")
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := decodeUniqueJSONObject(data, &settings); err != nil {
				return errors.New("Claude proxy settings must be one valid JSON object; refusing to overwrite them")
			}
		}
	} else if !os.IsNotExist(err) {
		return errors.New("could not inspect the Claude proxy settings")
	}
	if raw, ok := settings["forceLoginMethod"]; ok {
		var method string
		if json.Unmarshal(raw, &method) != nil || method != claudeNativeLoginMethod {
			return errors.New("Claude proxy settings select another login method; refusing to overwrite them")
		}
		if info != nil && fileprivacy.CheckFile(info) != nil {
			if err := fileprivacy.ChmodFile(path, fileprivacy.FileMode); err != nil {
				return errors.New("could not protect the Claude proxy settings")
			}
		}
		return nil
	}
	settings["forceLoginMethod"] = json.RawMessage(`"` + claudeNativeLoginMethod + `"`)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("could not prepare the Claude proxy settings")
	}
	data = append(data, '\n')
	if err := fileprivacy.WriteFileAtomic(path, data); err != nil {
		return errors.New("could not write the Claude proxy settings")
	}
	return nil
}

func decodeUniqueJSONObject(data []byte, out *map[string]json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	values := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("invalid JSON")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return errors.New("invalid JSON")
		}
		if _, duplicate := values[key]; duplicate {
			return errors.New("duplicate JSON member")
		}
		values[key] = raw
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("invalid JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON content")
	}
	*out = values
	return nil
}
