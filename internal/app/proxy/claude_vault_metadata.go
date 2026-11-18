package proxy

import (
	"sort"
	"strings"
)

const claudeVaultMetadataMaxString = 1024

// Native clients use these identity fields after exchange. Unknown nested
// fields must not become a second channel for issuer credentials or API keys.
type claudeVaultAccount struct {
	UUID         string `json:"uuid,omitempty"`
	EmailAddress string `json:"email_address,omitempty"`
}
type claudeVaultOrganization struct {
	UUID string `json:"uuid,omitempty"`
	Name string `json:"name,omitempty"`
}

func sanitizeClaudeVaultMetadata(reply *claudeVaultReply, previous *claudeVaultRecord) error {
	tokens := []string{reply.AccessToken, reply.RefreshToken}
	if previous != nil {
		tokens = append(tokens, previous.private.Access, previous.private.Refresh)
	}
	// Replace overlapping credentials longest first so a shorter token cannot
	// leave an exposed suffix of a longer known credential.
	sort.Slice(tokens, func(i, j int) bool { return len(tokens[i]) > len(tokens[j]) })
	var fields []*string
	if reply.Account != nil {
		fields = append(fields, &reply.Account.UUID, &reply.Account.EmailAddress)
	}
	if reply.Organization != nil {
		fields = append(fields, &reply.Organization.UUID, &reply.Organization.Name)
	}
	for _, field := range fields {
		if len(*field) > claudeVaultMetadataMaxString {
			return errClaudeVaultLogin
		}
		for _, token := range tokens {
			if token != "" {
				*field = strings.ReplaceAll(*field, token, "[redacted]")
			}
		}
		if len(*field) > claudeVaultMetadataMaxString {
			return errClaudeVaultLogin
		}
		// Even the fixed redaction marker must not accidentally echo a known token.
		for _, token := range tokens {
			if token != "" && strings.Contains(*field, token) {
				return errClaudeVaultLogin
			}
		}
	}
	return nil
}
