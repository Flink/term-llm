package proxyclient

import (
	"encoding/json"
	"strings"
	"time"
)

// Discovery grants a short client-side lease on the aliases, not on the
// vendor token. The proxy owns provider refresh and its actual expiration.
const agyMaxAliasLifetime = 3600

type agySharedIdentity struct {
	access, refresh string
	expiresIn       int64
}

func parseAgySharedIdentity(body []byte) (agySharedIdentity, error) {
	var fields map[string]json.RawMessage
	var identity agySharedIdentity
	if decodeUniqueJSONObject(body, &fields) != nil {
		return identity, agyNativePolicy.errorf("metadata response is invalid")
	}
	if fields["access_placeholder"] == nil && fields["refresh_placeholder"] == nil && fields["scopes"] == nil && fields["expires_in"] == nil {
		return identity, nil // Operator enrollment never advertises aliases.
	}
	if json.Unmarshal(fields["access_placeholder"], &identity.access) != nil || !validAgyAlias(identity.access, agyAccessPrefix) {
		return agySharedIdentity{}, agyNativePolicy.errorf("metadata access_placeholder is invalid")
	}
	if json.Unmarshal(fields["refresh_placeholder"], &identity.refresh) != nil || !validAgyAlias(identity.refresh, agyRefreshPrefix) {
		return agySharedIdentity{}, agyNativePolicy.errorf("metadata refresh_placeholder is invalid")
	}
	if json.Unmarshal(fields["expires_in"], &identity.expiresIn) != nil || identity.expiresIn < 1 || identity.expiresIn > agyMaxAliasLifetime {
		return agySharedIdentity{}, agyNativePolicy.errorf("metadata expires_in is invalid")
	}
	if fields["scopes"] != nil && !validAgyScopes(fields["scopes"]) {
		return agySharedIdentity{}, agyNativePolicy.errorf("metadata scopes are invalid")
	}
	return identity, nil
}

func validAgyScopes(raw json.RawMessage) bool {
	var scopes *string
	if json.Unmarshal(raw, &scopes) != nil || scopes == nil {
		return false
	}
	if *scopes != "" {
		for _, scope := range strings.Split(*scopes, " ") {
			if scope == "" || strings.IndexFunc(scope, func(r rune) bool { return r < '!' || r > '~' || r == '"' || r == '\\' }) >= 0 {
				return false
			}
		}
	}
	return true
}

func (i agySharedIdentity) seed(home string, now time.Time) error {
	if i.access == "" {
		return agyNativePolicy.errorf("metadata did not include a shared worker identity")
	}
	// Linux native storage uses the Go OAuth token envelope, not discovery's
	// scopes. Do not persist optional discovery metadata or account settings.
	type oauthToken struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		TokenType    string    `json:"token_type"`
		Expiry       time.Time `json:"expiry"`
		ExpiresIn    int64     `json:"expires_in"`
	}
	identity := struct {
		AuthMethod string     `json:"auth_method"`
		Token      oauthToken `json:"token"`
	}{"consumer", oauthToken{i.access, i.refresh, "Bearer", now.UTC().Add(time.Duration(i.expiresIn) * time.Second), i.expiresIn}}
	data, err := json.Marshal(identity)
	if err != nil {
		return agyNativePolicy.errorf("could not encode the shared worker identity")
	}
	return writeAgyNativeIdentity(home, data)
}
