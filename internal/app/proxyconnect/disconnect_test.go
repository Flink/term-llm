package proxyconnect

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	legacyGateway = "https://offline.example.test/claude/"
	legacyToken   = "synthetic-scoped-client-token"
)

type undoFixture struct {
	home, dir, path string
	original        []byte
	record          claudeReceiptFile
	settings        map[string]any
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func marshalTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Build historical on-disk records directly, without enrollment, preparation,
// executables, network, or access to any real Claude installation.
func newUndoFixture(t *testing.T, version int, created bool) *undoFixture {
	t.Helper()
	f := &undoFixture{home: t.TempDir(), settings: map[string]any{}}
	f.dir = filepath.Join(f.home, ".claude")
	f.path = filepath.Join(f.dir, "settings.json")
	if err := os.Mkdir(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]any{}
	if !created {
		env["EDITOR"] = "vi"
		f.settings["permissions"] = map[string]any{"allow": []any{"Bash(ls)"}}
		f.settings["hooks"] = map[string]any{"futureHook": []any{1.0, "keep"}}
		f.settings["env"] = env
		f.original = marshalTestJSON(t, f.settings)
		writeTestFile(t, filepath.Join(f.dir, claudeBackupName), f.original)
	}
	f.record = claudeReceiptFile{
		Version: version, GatewayURL: legacyGateway, TokenSHA256: tokenDigest(legacyToken),
		CreatedSettings: created, CreatedEnv: created, OriginalSHA256: tokenDigest(string(f.original)),
	}
	if version == 1 {
		env[claudeBaseURLKey], env[claudeAuthTokenKey] = legacyGateway, legacyToken
	} else {
		// The original native preparation explicitly removed these keys.
		removed := map[string]bool{
			claudeBaseURLKey: true, claudeAuthTokenKey: true, "ANTHROPIC_API_KEY": true,
			"CLAUDE_CODE_API_BASE_URL": true, "ALL_PROXY": true, "all_proxy": true,
			"https_proxy": true, "http_proxy": true,
		}
		f.record.OwnedEnv = make(map[string]claudeOwnedEnv)
		for _, key := range claudeNativeKeys {
			var raw json.RawMessage
			if !removed[key] {
				value := "synthetic-" + key
				if key == "CLAUDE_CODE_OAUTH_TOKEN" {
					value = legacyToken
				}
				env[key] = value
				raw = marshalTestJSON(t, value)
			}
			f.record.OwnedEnv[key] = claudeOwnedEnv{WrittenSHA256: nativeEnvDigest(raw)}
		}
	}
	f.settings["env"] = env
	f.record.UpdatedSHA256 = tokenDigest(string(marshalTestJSON(t, f.settings)))
	f.save(t)
	return f
}

func (f *undoFixture) save(t *testing.T) {
	t.Helper()
	writeTestFile(t, f.path, marshalTestJSON(t, f.settings))
	writeTestFile(t, filepath.Join(f.dir, claudeReceiptName), marshalTestJSON(t, f.record))
}

func (f *undoFixture) disconnect(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if err := Disconnect(Options{Home: f.home, Out: &out}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s absent, got %v", filepath.Base(path), err)
	}
}

func TestDisconnectLegacyPreservesUnrelatedEditsAndNativeCredentials(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(map[int]string{1: "baseURL", 2: "native"}[version], func(t *testing.T) {
			f := newUndoFixture(t, version, false)
			env := f.settings["env"].(map[string]any)
			env["EDITOR"], env["LATER"] = "emacs", "keep"
			f.settings["model"] = "later-model"
			f.settings["enabledPlugins"] = map[string]any{"third-party": true}
			if version == 1 {
				// A direct credential added later is not owned by a v1 receipt.
				env["CLAUDE_CODE_OAUTH_TOKEN"] = "synthetic-direct-credential"
			} else {
				owned := f.record.OwnedEnv["ANTHROPIC_API_KEY"]
				owned.Previous = json.RawMessage(`"synthetic-previous-direct-key"`)
				f.record.OwnedEnv["ANTHROPIC_API_KEY"] = owned
			}
			f.save(t)
			// Opaque vendor credentials and unrelated old metadata are not read,
			// parsed, or removed, even if an old pending record is malformed.
			for _, name := range []string{".credentials.json", "term-llm-connect-pending.json", "term-llm-connector.json"} {
				writeTestFile(t, filepath.Join(f.dir, name), []byte("opaque synthetic data"))
			}
			out := f.disconnect(t)
			if !strings.Contains(out, "manually in Clients") || strings.Contains(out, legacyToken) {
				t.Fatalf("unexpected output: %q", out)
			}
			var restored map[string]any
			if err := json.Unmarshal(readTestFile(t, f.path), &restored); err != nil {
				t.Fatal(err)
			}
			env = restored["env"].(map[string]any)
			if env["EDITOR"] != "emacs" || env["LATER"] != "keep" || restored["model"] != "later-model" || restored["hooks"] == nil || restored["permissions"] == nil || restored["enabledPlugins"] == nil {
				t.Fatal("undo lost unrelated settings")
			}
			if version == 1 && env["CLAUDE_CODE_OAUTH_TOKEN"] != "synthetic-direct-credential" || version == 2 && env["ANTHROPIC_API_KEY"] != "synthetic-previous-direct-key" {
				t.Fatal("undo lost direct credentials")
			}
			if _, ok := env[claudeBaseURLKey]; ok {
				t.Fatal("owned gateway remains")
			}
			for _, name := range []string{".credentials.json", "term-llm-connect-pending.json", "term-llm-connector.json"} {
				if string(readTestFile(t, filepath.Join(f.dir, name))) != "opaque synthetic data" {
					t.Fatal("undo changed unrelated private state")
				}
			}
			assertAbsent(t, filepath.Join(f.dir, claudeReceiptName))
			assertAbsent(t, filepath.Join(f.dir, claudeBackupName))
			before := readTestFile(t, f.path)
			f.disconnect(t)
			if !bytes.Equal(before, readTestFile(t, f.path)) {
				t.Fatal("repeated undo modified settings")
			}
		})
	}
}

func TestDisconnectCreatedSettings(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, laterEdit := range []bool{false, true} {
			f := newUndoFixture(t, version, true)
			if laterEdit {
				f.settings["model"] = "keep"
				f.save(t)
			}
			f.disconnect(t)
			if !laterEdit {
				assertAbsent(t, f.path)
			} else if string(readTestFile(t, f.path)) != "{\n  \"model\": \"keep\"\n}\n" {
				t.Fatal("undo removed or contaminated later settings")
			}
		}
	}
}

func TestDisconnectEveryOwnedKeyFailsClosed(t *testing.T) {
	for _, version := range []int{1, 2} {
		keys := []string{claudeBaseURLKey, claudeAuthTokenKey}
		if version == 2 {
			keys = claudeNativeKeys
		}
		for _, key := range keys {
			t.Run(map[int]string{1: "baseURL/", 2: "native/"}[version]+key, func(t *testing.T) {
				f := newUndoFixture(t, version, false)
				f.settings["env"].(map[string]any)[key] = "synthetic-third-party-edit"
				f.save(t)
				before := readTestFile(t, f.path)
				receipt := readTestFile(t, filepath.Join(f.dir, claudeReceiptName))
				err := Disconnect(Options{Home: f.home, Out: io.Discard})
				if !errors.Is(err, ErrClaudeSettingsChanged) || strings.Contains(err.Error(), "synthetic-third-party-edit") {
					t.Fatalf("unsafe conflict result: %v", err)
				}
				if !bytes.Equal(before, readTestFile(t, f.path)) || !bytes.Equal(receipt, readTestFile(t, filepath.Join(f.dir, claudeReceiptName))) || !bytes.Equal(f.original, readTestFile(t, filepath.Join(f.dir, claudeBackupName))) {
					t.Fatal("conflict modified settings or undo evidence")
				}
			})
		}
	}
}

func TestDisconnectNoReceiptIsReadOnly(t *testing.T) {
	for _, hasClaudeDir := range []bool{false, true} {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude")
		if hasClaudeDir {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(dir, "settings.json"), []byte("unowned settings, not even JSON"))
			writeTestFile(t, filepath.Join(dir, claudeBackupName), []byte("unowned backup"))
		}
		for range 2 {
			if err := Disconnect(Options{Home: home, Out: io.Discard}); err != nil {
				t.Fatal(err)
			}
		}
		if !hasClaudeDir {
			assertAbsent(t, dir)
		} else {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 || string(readTestFile(t, filepath.Join(dir, "settings.json"))) != "unowned settings, not even JSON" {
				t.Fatal("no-receipt undo changed unowned files")
			}
		}
	}
}

func TestDisconnectPreservesUnverifiableBackups(t *testing.T) {
	for _, kind := range []string{"replacement", "pre-hash", "symlink", "directory", "non-private"} {
		t.Run(kind, func(t *testing.T) {
			f := newUndoFixture(t, 1, false)
			backup := filepath.Join(f.dir, claudeBackupName)
			switch kind {
			case "replacement":
				writeTestFile(t, backup, []byte("third-party replacement"))
			case "pre-hash":
				f.record.OriginalSHA256, f.record.UpdatedSHA256 = "", ""
				f.save(t)
			case "symlink", "directory":
				if err := os.Remove(backup); err != nil {
					t.Fatal(err)
				}
				if kind == "directory" {
					if err := os.Mkdir(backup, 0o700); err != nil {
						t.Fatal(err)
					}
				} else {
					target := filepath.Join(f.home, "third-party")
					writeTestFile(t, target, f.original)
					if err := os.Symlink(target, backup); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				}
			case "non-private":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX permissions")
				}
				if err := os.Chmod(backup, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(backup)
			if err != nil {
				t.Fatal(err)
			}
			f.disconnect(t)
			after, err := os.Lstat(backup)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("undo removed or replaced an unverified backup")
			}
			if kind != "directory" {
				want := f.original
				if kind == "replacement" {
					want = []byte("third-party replacement")
				}
				if !bytes.Equal(want, readTestFile(t, backup)) {
					t.Fatal("undo changed backup contents")
				}
			}
		})
	}
}

func TestDisconnectRefusesSymlinks(t *testing.T) {
	for _, name := range []string{"home", ".claude", "settings.json", claudeReceiptName, ".term-llm-connect.lock"} {
		t.Run(name, func(t *testing.T) {
			f := newUndoFixture(t, 1, false)
			path := filepath.Join(f.dir, name)
			switch name {
			case "home":
				path = f.home + "-link"
				t.Cleanup(func() { _ = os.Remove(path) })
			case ".claude":
				path = f.dir
			}
			target := path + "-target"
			if name == "home" {
				target = f.home
			} else if name == ".term-llm-connect.lock" {
				writeTestFile(t, target, []byte("third-party lock"))
			} else if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			home := f.home
			if name == "home" {
				home = path
			}
			if err := Disconnect(Options{Home: home, Out: io.Discard}); err == nil {
				t.Fatal("undo accepted a symlink")
			}
			if _, err := os.Readlink(path); err != nil {
				t.Fatal("undo removed symlink")
			}
		})
	}
}

func TestDisconnectInterruptedIntentAndUpgrade(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, created := range []bool{false, true} {
			f := newUndoFixture(t, version, created)
			if created {
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, f.path, f.original)
			}
			f.disconnect(t)
			if created {
				assertAbsent(t, f.path)
			} else if !bytes.Equal(f.original, readTestFile(t, f.path)) {
				t.Fatal("unapplied intent rewrote settings")
			}
			assertAbsent(t, filepath.Join(f.dir, claudeReceiptName))
		}
	}
	// A v2 intent journaled over still-applied v1 settings must undo the v1
	// keys using the pre-v1 values, not apply or restore native overrides.
	f := newUndoFixture(t, 1, false)
	legacy := f.record
	legacy.PreviousBaseURL = json.RawMessage(`"https://previous.example.test"`)
	legacy.PreviousToken = json.RawMessage(`"synthetic-previous-token"`)
	native := newUndoFixture(t, 2, false)
	f.record = native.record
	f.record.Legacy = &legacy
	f.record.UpgradeSHA256 = tokenDigest(string(marshalTestJSON(t, f.settings)))
	f.save(t)
	f.disconnect(t)
	var restored map[string]any
	if err := json.Unmarshal(readTestFile(t, f.path), &restored); err != nil {
		t.Fatal(err)
	}
	env := restored["env"].(map[string]any)
	if env[claudeBaseURLKey] != "https://previous.example.test" || env[claudeAuthTokenKey] != "synthetic-previous-token" || env["EDITOR"] != "vi" {
		t.Fatal("interrupted native upgrade lost pre-v1 values")
	}
}

func TestDisconnectWriteFailurePreservesEvidence(t *testing.T) {
	f := newUndoFixture(t, 2, false)
	before := readTestFile(t, f.path)
	receipt := readTestFile(t, filepath.Join(f.dir, claudeReceiptName))
	originalWrite := writeClaudeFile
	t.Cleanup(func() { writeClaudeFile = originalWrite })
	writeClaudeFile = func(string, []byte) error { return errors.New("synthetic write failure") }
	if err := Disconnect(Options{Home: f.home, Out: io.Discard}); err == nil {
		t.Fatal("write failure ignored")
	}
	if !bytes.Equal(before, readTestFile(t, f.path)) || !bytes.Equal(f.original, readTestFile(t, filepath.Join(f.dir, claudeBackupName))) {
		t.Fatal("failed undo damaged settings or backup")
	}
	if !bytes.Equal(receipt, readTestFile(t, filepath.Join(f.dir, claudeReceiptName))) {
		t.Fatal("failed undo damaged its receipt")
	}
	writeClaudeFile = fileprivacy.WriteFileAtomic
	f.disconnect(t)
}

func TestDisconnectRejectsInvalidEvidenceAndSettings(t *testing.T) {
	for _, kind := range []string{"receipt-version", "receipt-token-hash", "receipt-owned-key", "receipt-duplicate", "receipt-non-private", "settings-invalid", "settings-duplicate", "env-not-object", "missing-settings"} {
		t.Run(kind, func(t *testing.T) {
			f := newUndoFixture(t, 2, false)
			receiptPath := filepath.Join(f.dir, claudeReceiptName)
			switch kind {
			case "receipt-version":
				f.record.Version = 99
				f.save(t)
			case "receipt-token-hash":
				f.record.TokenSHA256 = "not-a-hash"
				f.save(t)
			case "receipt-owned-key":
				delete(f.record.OwnedEnv, "HTTPS_PROXY")
				f.save(t)
			case "receipt-duplicate":
				raw := readTestFile(t, receiptPath)
				writeTestFile(t, receiptPath, bytes.Replace(raw, []byte("{"), []byte(`{"version":1,`), 1))
			case "receipt-non-private":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX permissions")
				}
				if err := os.Chmod(receiptPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "settings-invalid":
				writeTestFile(t, f.path, []byte("[not JSON"))
			case "settings-duplicate":
				raw := readTestFile(t, f.path)
				writeTestFile(t, f.path, bytes.Replace(raw, []byte("{"), []byte(`{"env":{},`), 1))
			case "env-not-object":
				writeTestFile(t, f.path, []byte(`{"env":null}`))
			case "missing-settings":
				if err := os.Remove(f.path); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(f.path)
			receipt := readTestFile(t, receiptPath)
			if err := Disconnect(Options{Home: f.home, Out: io.Discard}); err == nil {
				t.Fatal("unsafe undo accepted")
			}
			after, _ := os.ReadFile(f.path)
			if !bytes.Equal(before, after) || !bytes.Equal(receipt, readTestFile(t, receiptPath)) || !bytes.Equal(f.original, readTestFile(t, filepath.Join(f.dir, claudeBackupName))) {
				t.Fatal("refused undo modified settings or evidence")
			}
		})
	}
}

func TestDisconnectCompletedNativeUpgradeRestoresPreEnrollmentValues(t *testing.T) {
	f := newUndoFixture(t, 2, false)
	legacy := claudeReceiptFile{Version: 1, GatewayURL: legacyGateway, TokenSHA256: tokenDigest(legacyToken)}
	f.record.Legacy, f.record.UpgradeSHA256 = &legacy, tokenDigest("synthetic pre-upgrade settings")
	previous := map[string]string{
		claudeBaseURLKey: "https://previous.example.test", claudeAuthTokenKey: "synthetic-original-bearer",
		"CLAUDE_CODE_OAUTH_TOKEN": "synthetic-original-native-token", "HTTPS_PROXY": "http://previous.example.test",
	}
	for key, value := range previous {
		owned := f.record.OwnedEnv[key]
		owned.Previous = marshalTestJSON(t, value)
		f.record.OwnedEnv[key] = owned
	}
	f.save(t)
	f.disconnect(t)
	var restored map[string]any
	if err := json.Unmarshal(readTestFile(t, f.path), &restored); err != nil {
		t.Fatal(err)
	}
	env := restored["env"].(map[string]any)
	for key, value := range previous {
		if env[key] != value {
			t.Fatalf("pre-enrollment %s not restored", key)
		}
	}
}
