package proxyclient

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	claudeOAuthTokenEnv  = "CLAUDE_CODE_OAUTH_TOKEN"
	claudeOAuthFDEnv     = "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"
	claudeOAuthScopesEnv = "CLAUDE_CODE_OAUTH_SCOPES"
	// A bounded write fits in the empty pipe before the child starts reading.
	claudeMaxPlaceholderBytes = 512
)

var claudePlaceholderPattern = regexp.MustCompile(`^sk-ant-oat01-tlv2-[A-Za-z0-9_-]+$`)

type claudeNativeAccess struct {
	placeholder string
	scopes      string
}

func parseClaudeNativeAccess(body []byte) (claudeNativeAccess, error) {
	var metadata struct {
		AccessPlaceholder json.RawMessage `json:"access_placeholder"`
		Scopes            json.RawMessage `json:"scopes"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return claudeNativeAccess{}, errors.New("Claude proxy metadata response is invalid")
	}
	if len(metadata.AccessPlaceholder) == 0 {
		return claudeNativeAccess{}, nil // Classic operator login bootstrap.
	}
	var placeholder, scopes string
	if json.Unmarshal(metadata.AccessPlaceholder, &placeholder) != nil || len(placeholder) > claudeMaxPlaceholderBytes || !claudePlaceholderPattern.MatchString(placeholder) {
		return claudeNativeAccess{}, errors.New("Claude proxy metadata access_placeholder is invalid")
	}
	if len(metadata.Scopes) != 0 && json.Unmarshal(metadata.Scopes, &scopes) != nil {
		return claudeNativeAccess{}, errors.New("Claude proxy metadata scopes are invalid")
	}
	// OAuth scopes are opaque printable tokens separated by single spaces.
	if scopes != "" {
		for _, scope := range strings.Split(scopes, " ") {
			if scope == "" || strings.IndexFunc(scope, invalidClaudeScopeChar) >= 0 {
				return claudeNativeAccess{}, errors.New("Claude proxy metadata scopes are invalid")
			}
		}
	}
	return claudeNativeAccess{placeholder: placeholder, scopes: scopes}, nil
}

func invalidClaudeScopeChar(r rune) bool { return r < '!' || r > '~' || r == '"' || r == '\\' }

// apply adds only the scoped placeholder, never a vendor or refresh token.
// The pipe has no filesystem path and the writer is closed before exec, so
// Claude can read to EOF. The caller owns the read end through command launch.
func (a claudeNativeAccess) apply(cmd *exec.Cmd) (cleanup func(), err error) {
	if a.placeholder == "" {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		// os/exec cannot pass ExtraFiles on Windows.
		cmd.Env = append(cmd.Env, claudeOAuthTokenEnv+"="+a.placeholder)
		key := len(cmd.Env) - 1
		cleanup = func() { cmd.Env[key] = claudeOAuthTokenEnv + "=" }
	} else {
		reader, writer, err := os.Pipe()
		if err != nil {
			return nil, errors.New("Claude proxy could not open the OAuth placeholder pipe")
		}
		_, writeErr := writer.WriteString(a.placeholder)
		closeErr := writer.Close()
		if writeErr != nil || closeErr != nil {
			_ = reader.Close()
			return nil, errors.New("Claude proxy could not prepare the OAuth placeholder pipe")
		}
		fd := 3 + len(cmd.ExtraFiles)
		cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
		cmd.Env = append(cmd.Env, claudeOAuthFDEnv+"="+strconv.Itoa(fd))
		cleanup = func() { _ = reader.Close() }
	}
	if a.scopes != "" {
		cmd.Env = append(cmd.Env, claudeOAuthScopesEnv+"="+a.scopes)
	}
	return cleanup, nil
}
