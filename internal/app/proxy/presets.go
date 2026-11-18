package proxy

import "github.com/term-llm/term-llm/internal/proxyprofile"

// routePreset only supplies editable creation-time defaults. Forwarding and
// validation remain provider-independent; no credentials or client state live here.
//
// Auth (on the preset or each rule) is the local client authentication format.
// It is also the upstream credential substitution, unless AccountKind names a
// trusted account or Auth is empty (no upstream key).
type routePreset struct {
	Rules       []routePresetRule `json:"rules,omitempty"`
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Upstream    string            `json:"upstream"`
	Auth        Auth              `json:"auth"`
	Note        string            `json:"note,omitempty"`
	Billing     string            `json:"billing,omitempty"`
	Usage       string            `json:"usage,omitempty"`
	WebSocket   bool              `json:"websocket,omitempty"`
	AccountKind string            `json:"account_kind,omitempty"`
	// Group is the picker section: plan, api_key, self_hosted or media.
	Group string `json:"group"`
	// Companions are presets created beside this one so ancillary client
	// features (dictation, embeddings, live, CLI) reach their fixed route names.
	// A companion is never offered on its own in the picker.
	Companions []string `json:"companions,omitempty"`
}

type routePresetRule struct {
	PathPrefix string `json:"path_prefix"`
	Upstream   string `json:"upstream"`
	Auth       Auth   `json:"auth"`
}

// routePresets projects the native profiles, combining OpenCode Go's protocols
// only for new creation. Existing single-upstream routes remain unchanged.
var routePresets = presetsFromProfiles(proxyprofile.All())

// presetCompanions names the sibling routes a term-llm client expects beside
// each provider. Clients bind them by route name, so they stay separate routes.
var presetCompanions = map[string][]string{
	"chatgpt": {"chatgpt-backend", "chatgpt-openai"},
	"gemini":  {"gemini-live"},
	"grok":    {"grok-bin"},
}

// presetNotes holds operator guidance that the routing table does not carry.
var presetNotes = map[string]string{
	"openai":            "Set your client's base URL to this route's local prefix, without adding /v1 again.",
	"anthropic":         "The client sends anthropic-version and beta headers unchanged. Put the client token in the client's API key field.",
	"gemini":            "Use a header-based API key, not ?key=. The local route replaces the /v1beta API root.",
	"gemini-live":       "Use a header-based API key, not ?key=.",
	"xai":               "API key access, not a Grok plan login.",
	"chatgpt":           "Only the Codex responses, models, image and realtime-call endpoints are forwarded.",
	"chatgpt-backend":   "Only plan usage and transcription endpoints are forwarded.",
	"chatgpt-openai":    "Only embeddings and realtime endpoints are forwarded. How OpenAI bills these for a ChatGPT login is not established, so billing stays unspecified.",
	"grok":              "Only the responses, chat completions, models and billing endpoints are forwarded.",
	"copilot":           "Only the responses, chat completions and models endpoints are forwarded.",
	"claude-code":       "Use Set up Claude, then sign in normally with term-llm proxy run claude.",
	"bedrock":           "Enter https://bedrock-runtime.<region>.amazonaws.com for the region stored with the account. The proxy signs each request; only model invoke and converse calls are forwarded.",
	"opencode-go":       "One key, one grant, three protocols: Chat Completions and Responses at /, Messages at /messages/, and Gemini at /gemini/. The client still sends its own x-opencode-client and x-opencode-session headers.",
	"elevenlabs":        "The client appends /v1 speech or music paths. Put the client token in its API key field.",
	"ollama":            "Localhost is the proxy machine. Acknowledge HTTP below. No upstream key is substituted; clients still authenticate to the proxy with their client token.",
	"vllm":              "Enter your server URL. Upstream bearer authentication is optional: remove the substitution if the server has no key.",
	"openai-compatible": "Enter your server URL. Upstream bearer authentication is optional: remove the substitution if the server has no key.",
}

func presetsFromProfiles(profiles []proxyprofile.Profile) []routePreset {
	out := make([]routePreset, 0, len(profiles))
	for _, p := range profiles {
		// A gated profile has no verified credentialless path; offering it would
		// claim an integration the proxy cannot deliver. An account kind without
		// proxy custody support cannot be selected either.
		if p.Gate != "" || p.Account != "" && !accountKind(p.Account) || p.ID == "opencode-go-messages" || p.ID == "opencode-go-gemini" {
			continue
		}
		preset := routePreset{ID: p.ID, Label: p.Label, Upstream: p.Upstream, Auth: Auth{Header: p.Header, Prefix: p.Prefix}, Note: presetNotes[p.ID], Billing: p.Billing, WebSocket: p.WebSocket, AccountKind: p.Account}
		if p.ID == "opencode-go" {
			preset.Label, preset.Upstream, preset.Auth = "OpenCode Go", "", Auth{}
			preset.Rules = []routePresetRule{
				{PathPrefix: "/", Upstream: "https://opencode.ai/zen/go/v1", Auth: Auth{Header: "Authorization", Prefix: "Bearer "}},
				{PathPrefix: "/messages/", Upstream: "https://opencode.ai/zen/go", Auth: Auth{Header: "x-api-key"}},
				{PathPrefix: "/gemini/", Upstream: "https://opencode.ai/inference/go/v1", Auth: Auth{Header: "x-goog-api-key"}},
			}
		}
		if p.Billing == "local" {
			preset.Auth = Auth{} // Default bearer client authentication, no upstream key.
		}
		if p.Provider != "" {
			preset.Usage = "auto"
		}
		if p.Account != "" && p.Account != "claude-code" {
			preset.Note = joinNote("Uses a trusted "+p.Account+" account held by the proxy; choose it below. No API key is stored on the route.", preset.Note)
		}
		if p.WebSocket {
			preset.Note = joinNote(preset.Note, "WebSocket upgrades are forwarded for this route.")
		}
		if p.PathSuffix != "" {
			preset.Note = joinNote(preset.Note, "Full-endpoint clients append "+p.PathSuffix+" to the route URL.")
		}
		preset.Group = presetGroup(p)
		out = append(out, preset)
	}
	offered := make(map[string]bool, len(out))
	for _, preset := range out {
		offered[preset.ID] = true
	}
	for i := range out {
		for _, id := range presetCompanions[out[i].ID] {
			if !offered[id] {
				continue
			}
			out[i].Companions = append(out[i].Companions, id)
			for j := range out {
				if out[j].ID == id {
					out[j].Group = out[i].Group
				}
			}
		}
	}
	return out
}

func presetGroup(p proxyprofile.Profile) string {
	switch {
	case p.Billing == "plan":
		return "plan"
	case p.Billing == "local" || p.Provider != "" && p.Upstream == "" && p.Account == "":
		return "self_hosted"
	case p.Provider == "":
		return "media"
	default:
		return "api_key"
	}
}

func joinNote(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " " + b
}
