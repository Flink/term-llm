package proxyclient

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	agyCredentialFilename = "antigravity-oauth-token"
	agyAccessPrefix       = "agy-access-tlv2-"
	agyRefreshPrefix      = "agy-refresh-tlv2-"
)

func validAgyAlias(value, prefix string) bool {
	digest, ok := strings.CutPrefix(value, prefix)
	encoding := base64.RawURLEncoding.Strict()
	if !ok || len(digest) != encoding.EncodedLen(sha256.Size) {
		return false
	}
	decoded, err := encoding.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func agyCredentialDir(home string, create bool) (string, error) {
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, path := range []string{home, filepath.Join(home, ".gemini"), dir} {
		if create {
			if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
		if err := checkNativePrivateDir(path); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeAgyNativeIdentity installs the envelope generated from validated
// discovery aliases. It never reads existing tokens and replaces symlinks
// at the destination rather than following them.
func writeAgyNativeIdentity(home string, data []byte) error {
	dir, err := agyCredentialDir(home, true)
	if err != nil || fileprivacy.WriteFileAtomic(filepath.Join(dir, agyCredentialFilename), data) != nil {
		return agyNativePolicy.errorf("could not install the private vault placeholders")
	}
	return nil
}

func RemoveAgyWorkerIdentity(home string) error {
	dir, err := agyCredentialDir(home, false)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, agyCredentialFilename))
}
