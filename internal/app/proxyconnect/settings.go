package proxyconnect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	claudeSettingsMaxBytes = 1 << 20
	claudeBaseURLKey       = "ANTHROPIC_BASE_URL"
	claudeAuthTokenKey     = "ANTHROPIC_AUTH_TOKEN"
	claudeBackupName       = "settings.json.term-llm-proxy-backup"
	claudeReceiptName      = "term-llm-proxy-connect.json"
)

// ErrClaudeSettingsChanged reports that legacy settings no longer match their
// undo receipt. Undo preserves the settings and receipt for manual inspection.
var ErrClaudeSettingsChanged = errors.New("Claude settings changed; nothing was modified")

// claudeReceiptFile is the private undo record. Previous values are raw JSON;
// null means an owned key was absent. Version 1 owns the two API gateway keys;
// version 2 owns every native authentication, routing and trust override.
type claudeReceiptFile struct {
	Version         int             `json:"version"`
	GatewayURL      string          `json:"gateway_url"`
	TokenSHA256     string          `json:"token_sha256"`
	CreatedSettings bool            `json:"created_settings"`
	CreatedEnv      bool            `json:"created_env"`
	PreviousBaseURL json.RawMessage `json:"previous_base_url"`
	PreviousToken   json.RawMessage `json:"previous_auth_token"`
	// Hashes of settings.json before and after the change. They let an
	// interrupted Apply be recognised and resumed or cleared. Records written
	// before these existed leave them empty and cannot be resumed.
	OriginalSHA256 string `json:"original_sha256,omitempty"`
	UpdatedSHA256  string `json:"updated_sha256,omitempty"`
	// Version 2 authenticates every native routing/trust/credential env key,
	// including keys deliberately removed. Previous values remain private.
	OwnedEnv map[string]claudeOwnedEnv `json:"owned_env,omitempty"`
	// A migration records both its pre-upgrade document and the v1 receipt.
	// A crash after recording intent can therefore resume or undo safely.
	UpgradeSHA256 string             `json:"upgrade_sha256,omitempty"`
	Legacy        *claudeReceiptFile `json:"legacy,omitempty"`
}

type claudeOwnedEnv struct {
	Previous      json.RawMessage `json:"previous"`
	WrittenSHA256 string          `json:"written_sha256"`
}

// matchesOriginal reports whether settings.json is still exactly what it was
// before the recorded change, including not existing at all.
func (r claudeReceiptFile) matchesOriginal(current []byte, exists bool) bool {
	return r.OriginalSHA256 != "" && exists != r.CreatedSettings && tokenDigest(string(current)) == r.OriginalSHA256
}

// writeClaudeFile replaces a private file atomically. An error does not prove
// the file is unchanged: the rename may have succeeded before a later sync failed.
var writeClaudeFile = fileprivacy.WriteFileAtomic

// orderedObject is a JSON object that keeps member order and raw values, so
// unrelated settings survive a rewrite untouched apart from whitespace.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func parseOrderedObject(data []byte) (*orderedObject, error) {
	object := &orderedObject{values: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return object, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid JSON")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid JSON")
		}
		if _, duplicate := object.values[key]; duplicate {
			return nil, errors.New("duplicate JSON member")
		}
		object.keys = append(object.keys, key)
		object.values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("invalid JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	return object, nil
}

func (o *orderedObject) set(key string, value json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *orderedObject) remove(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, existing := range o.keys {
		if existing == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			return
		}
	}
}

func (o *orderedObject) compact() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(key)
		b.Write(name)
		b.WriteByte(':')
		b.Write(o.values[key])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (o *orderedObject) document() []byte {
	var b bytes.Buffer
	_ = json.Indent(&b, o.compact(), "", "  ")
	b.WriteByte('\n')
	return b.Bytes()
}

// jsonString returns the string held by a raw JSON value, if it is one.
func jsonString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// claudeSettingsDir validates home and returns the selected user's Claude
// directory. Neither home nor the directory may be a symlink.
func claudeSettingsDir(home string) (string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", errors.New("home must be a clean absolute path")
	}
	if info, err := os.Lstat(home); err != nil || !info.IsDir() {
		return "", errors.New("home is not a directory")
	}
	dir := filepath.Join(home, ".claude")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return dir, nil
	}
	if err != nil || !info.IsDir() {
		return "", errors.New("~/.claude must be a real directory, not a symlink or file")
	}
	if !ownedByCurrentUser(info) {
		return "", errors.New("~/.claude is owned by another user")
	}
	return dir, nil
}

// readPrivateRegular reads a bounded regular file. A missing file is (nil,
// false, nil); a symlink or any other non-regular file is refused.
func readPrivateRegular(path, label string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s must be a regular file, not a symlink or special file", label)
	}
	if !ownedByCurrentUser(info) {
		return nil, false, fmt.Errorf("%s is owned by another user", label)
	}
	if info.Size() > claudeSettingsMaxBytes {
		return nil, false, fmt.Errorf("%s is too large", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", label, err)
	}
	return data, true, nil
}

// envOf returns the settings env object and whether the member exists.
func envOf(settings *orderedObject) (*orderedObject, bool, error) {
	raw, ok := settings.values["env"]
	if !ok {
		return &orderedObject{values: map[string]json.RawMessage{}}, false, nil
	}
	env, err := parseOrderedObject(raw)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, true, errors.New("settings.json env is not a JSON object")
	}
	return env, true, nil
}

// ownBackup reports whether the backup file is absent, or is ours: a regular
// file holding exactly the recorded original settings.
func ownBackup(path, originalSHA256 string) (present, own bool) {
	data, exists, err := readPrivateRegular(path, "settings backup")
	if err != nil {
		return true, false
	}
	info, statErr := os.Lstat(path)
	return exists, exists && statErr == nil && fileprivacy.CheckFile(info) == nil &&
		originalSHA256 != "" && tokenDigest(string(data)) == originalSHA256
}

// restoreClaudeGatewayKeys restores owned env keys to their recorded earlier
// values and drops the env object or file only if Apply created them.
func restoreClaudeGatewayKeys(settings, env *orderedObject, record claudeReceiptFile, settingsPath string) error {
	previousValues := map[string]json.RawMessage{claudeBaseURLKey: record.PreviousBaseURL, claudeAuthTokenKey: record.PreviousToken}
	if record.Version == 2 {
		previousValues = make(map[string]json.RawMessage)
		for key, owned := range record.OwnedEnv {
			previousValues[key] = owned.Previous
		}
	}
	for key, previous := range previousValues {
		if len(previous) == 0 || string(previous) == "null" {
			env.remove(key)
		} else {
			env.set(key, previous)
		}
	}
	if len(env.keys) == 0 && record.CreatedEnv {
		settings.remove("env")
	} else {
		settings.set("env", env.compact())
	}
	if len(settings.keys) == 0 && record.CreatedSettings {
		return os.Remove(settingsPath)
	}
	return writeClaudeFile(settingsPath, settings.document())
}

// undoClaudeSettings restores only receipt-owned keys, never the whole backup.
// Later unrelated edits survive. A changed owned key refuses the entire undo;
// an interrupted legacy apply or upgrade is cleared using its recorded hashes.
func undoClaudeSettings(home string) error {
	dir, err := claudeSettingsDir(home)
	if err != nil {
		return err
	}
	receiptPath, settingsPath := filepath.Join(dir, claudeReceiptName), filepath.Join(dir, "settings.json")
	data, recorded, err := readPrivateRegular(receiptPath, "connection record")
	if err != nil {
		return err
	}
	var record claudeReceiptFile
	if !recorded {
		return nil
	}
	info, err := os.Lstat(receiptPath)
	if err != nil || fileprivacy.CheckFile(info) != nil {
		return errors.New("connection record must be private and owner-only")
	}
	if _, err := parseOrderedObject(data); err != nil {
		return errors.New("connection record is not valid; nothing was modified")
	}
	if json.Unmarshal(data, &record) != nil || !validClaudeReceipt(record) {
		return errors.New("connection record is not valid; nothing was modified")
	}
	current, exists, err := readPrivateRegular(settingsPath, "settings.json")
	if err != nil {
		return err
	}
	settings, err := parseOrderedObject(current)
	if err != nil {
		return fmt.Errorf("settings.json is not valid: %w", err)
	}
	env, _, err := envOf(settings)
	if err != nil {
		return err
	}
	switch {
	case exists && record.ownsEnv(env):
		if err := restoreClaudeGatewayKeys(settings, env, record, settingsPath); err != nil {
			return fmt.Errorf("restore settings.json: %w", err)
		}
	case record.upgrading(current, env):
		if err := restoreClaudeGatewayKeys(settings, env, *record.Legacy, settingsPath); err != nil {
			return fmt.Errorf("restore settings.json: %w", err)
		}
	case record.matchesOriginal(current, exists):
		// Intent was recorded but settings.json never changed.
	default:
		return fmt.Errorf("%w: the gateway settings were edited after they were applied", ErrClaudeSettingsChanged)
	}
	// Pre-hash receipts cannot prove backup ownership. Preserve those backups,
	// and any replacement, symlink, or other collision at the backup path.
	backupPath := filepath.Join(dir, claudeBackupName)
	if present, own := ownBackup(backupPath, record.OriginalSHA256); present && own && !record.CreatedSettings {
		if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", claudeBackupName, err)
		}
	}
	if err := os.Remove(receiptPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", claudeReceiptName, err)
	}
	return nil
}
