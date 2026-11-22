package deviceflow

// Reconnect is public capability metadata, never an account identifier or endpoint.
type Reconnect string

const (
	Eligible        Reconnect = "eligible"
	CustomSettings  Reconnect = "custom_settings"
	MissingIdentity Reconnect = "missing_identity"
)

func ReconnectEligibility(kind string, customSettings, pinned bool) Reconnect {
	switch {
	case kind != "chatgpt" && kind != "grok":
		return ""
	case customSettings:
		return CustomSettings
	case !pinned:
		return MissingIdentity
	default:
		return Eligible
	}
}
