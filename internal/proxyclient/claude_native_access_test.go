package proxyclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	claudeSharedTestPlaceholder = "sk-ant-oat01-tlv2-scoped-test-placeholder"
	claudeSharedTestScopes      = "user:profile user:inference"
	claudeSharedTestOrigin      = "https://proxy.example.test"
)

func parseClaudeTestBootstrap(t *testing.T, metadata map[string]any) (*nativeBootstrap, error) {
	t.Helper()
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return claudeNativePolicy.parseBootstrap(claudeSharedTestOrigin+"/team/claude-code", body)
}

func TestClaudeNativeSharedBootstrapRejectsInvalidAccess(t *testing.T) {
	ca := claudeNativeTestCA(t, nil)
	for _, tt := range []struct {
		name, placeholder, scopes, invalidField string
	}{
		{"empty placeholder", "", "", "access_placeholder"},
		{"vendor token", "sk-ant-oat01-real-vendor-token", "", "access_placeholder"},
		{"refresh placeholder", "sk-ant-ort01-tlv2-refresh", "", "access_placeholder"},
		{"trailing newline", claudeSharedTestPlaceholder + "\n", "", "access_placeholder"},
		{"oversize", claudeSharedTestPlaceholder + strings.Repeat("x", claudeMaxPlaceholderBytes), "", "access_placeholder"},
		{"control in scopes", claudeSharedTestPlaceholder, "user:inference\x00hidden", "scopes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata := claudeNativeTestMetadata(ca, claudeSharedTestOrigin, "claude-code")
			metadata["access_placeholder"], metadata["scopes"] = tt.placeholder, tt.scopes
			if _, err := parseClaudeTestBootstrap(t, metadata); err == nil || !strings.Contains(err.Error(), tt.invalidField) || (tt.placeholder != "" && strings.Contains(err.Error(), tt.placeholder)) {
				t.Fatalf("invalid or unsafe bootstrap result: %v", err)
			}
		})
	}
}

func TestClaudeNativeCommandHandoffOffline(t *testing.T) {
	ca := claudeNativeTestCA(t, nil)
	for _, tt := range []struct {
		name, placeholder, scopes string
	}{
		{"shared", claudeSharedTestPlaceholder, claudeSharedTestScopes},
		{"shared without scopes", claudeSharedTestPlaceholder, ""},
		{"operator login", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata := claudeNativeTestMetadata(ca, claudeSharedTestOrigin, "claude-code")
			if tt.placeholder != "" {
				metadata["access_placeholder"] = tt.placeholder
			}
			if tt.scopes != "" {
				metadata["scopes"] = tt.scopes
			}
			bootstrap, err := parseClaudeTestBootstrap(t, metadata)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestClaudeNativeFakeChild$")
			ambient := []string{
				"CLAUDE_NATIVE_FAKE_CHILD=1", "UNRELATED=kept", "HTTPS_PROXY=http://ambient.invalid", "NO_PROXY=*",
				"ANTHROPIC_API_KEY=vendor-key", "CLAUDE_CONFIG_DIR=/ambient", "TERM_LLM_CLAUDE_PROXY_ROOT=/ambient",
				claudeOAuthTokenEnv + "=vendor-access", claudeOAuthFDEnv + "=50", claudeOAuthScopesEnv + "=stale:scope",
				"CLAUDE_CODE_OAUTH_REFRESH_TOKEN=vendor-refresh", "CLAUDE_CODE_OAUTH_TOKEN_FILE=/ambient/token",
			}
			cmd.Env = ambient
			if tt.placeholder != "" {
				cmd.Env = append(cmd.Env, "CLAUDE_CODE_SIMPLE=1")
			}
			if runtime.GOOS != "windows" {
				// Ensure the OAuth descriptor does not clobber an existing ExtraFile.
				file, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				cmd.ExtraFiles = append(cmd.ExtraFiles, file)
			}
			stateDir := t.TempDir()
			cleanup, err := prepareClaudeNativeCommand(cmd, bootstrap, claudeNativeTestToken, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("launch fake Claude: %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("decode child output: %v", err)
			}
			if got["read_token"] != tt.placeholder || got[claudeOAuthScopesEnv] != tt.scopes {
				t.Fatal("child did not see the expected scoped credential")
			}
			assertClaudeChildIsIsolated(t, got, stateDir)
			if tt.placeholder == "" {
				if cleanup != nil || got[claudeOAuthFDEnv] != "" || got[claudeOAuthTokenEnv] != "" {
					t.Fatal("operator login acquired an injected credential")
				}
				return
			}
			if cleanup == nil {
				t.Fatal("scoped credential did not provide cleanup")
			}
			if runtime.GOOS != "windows" {
				if got[claudeOAuthFDEnv] != "4" || got[claudeOAuthTokenEnv] != "" || len(cmd.ExtraFiles) != 2 {
					t.Fatal("placeholder not isolated on the inherited descriptor")
				}
				cleanup()
				if _, err := cmd.ExtraFiles[1].Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("parent descriptor leaked: %v", err)
				}
			}
		})
	}
}

func assertClaudeChildIsIsolated(t *testing.T, got map[string]string, stateDir string) {
	t.Helper()
	for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE", "CLAUDE_CODE_SIMPLE", "TERM_LLM_CLAUDE_PROXY_ROOT"} {
		if got[key] != "" {
			t.Errorf("child inherited %s", key)
		}
	}
	if got["CLAUDE_CONFIG_DIR"] != stateDir || got["NODE_EXTRA_CA_CERTS"] != filepath.Join(stateDir, claudeNativeCAFile) || got["UNRELATED"] != "kept" {
		t.Fatal("child did not receive the isolated Claude profile and CA")
	}
	proxy, err := url.Parse(got["HTTPS_PROXY"])
	if err != nil || proxy.Host != "proxy.example.test" || proxy.User.Username() != "claude-code" || got["NO_PROXY"] == "*" {
		t.Fatal("child did not receive the scoped Claude tunnel")
	}
}

// The subprocess substitutes for Claude; it makes no network calls and emits
// only preselected fixture values. Exit directly to suppress test-runner PASS.
func TestClaudeNativeFakeChild(t *testing.T) {
	if os.Getenv("CLAUDE_NATIVE_FAKE_CHILD") != "1" {
		return
	}
	keys := []string{
		"UNRELATED", "HTTPS_PROXY", "NO_PROXY", "CLAUDE_CONFIG_DIR", "NODE_EXTRA_CA_CERTS", "CLAUDE_CODE_SIMPLE",
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE", "TERM_LLM_CLAUDE_PROXY_ROOT",
		claudeOAuthFDEnv, claudeOAuthScopesEnv, claudeOAuthTokenEnv,
	}
	got := map[string]string{}
	for _, key := range keys {
		got[key] = os.Getenv(key)
	}
	got["read_token"] = got[claudeOAuthTokenEnv]
	if fd, err := strconv.Atoi(got[claudeOAuthFDEnv]); err == nil {
		file := os.NewFile(uintptr(fd), "oauth")
		token, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			os.Exit(2)
		}
		got["read_token"] = string(token)
	}
	if err := json.NewEncoder(os.Stdout).Encode(got); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
