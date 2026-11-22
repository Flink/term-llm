package proxy

import (
	"slices"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

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
	Family      string            `json:"family"`
	Allow       []string          `json:"allow,omitempty"`
	// Companions are presets created beside this one so ancillary client
	// features (dictation, embeddings, live) reach their fixed route names.
	// A companion is never offered on its own in the picker.
	Companions []string `json:"companions,omitempty"`
}

type routePresetRule struct {
	PathPrefix string   `json:"path_prefix"`
	Upstream   string   `json:"upstream"`
	Auth       Auth     `json:"auth"`
	Allow      []string `json:"allow,omitempty"`
}

// routePresets projects the profiles offered for new routes. Existing routes
// continue to use their original profiles for validation and client discovery.
var routePresets = presetsFromProfiles(proxyprofile.All())

// presetNotes holds operator guidance that the routing table does not carry.
var presetNotes = map[string]string{
	"typesafe":          "Runs Jev classification. Use this route's local prefix as the classifier base URL, without adding /v1.",
	"openai-decisions":  "This route serves Decisions only. If needed, set OpenAI organization and project headers on the route.",
	"cloudflare-clef":   "Enter the full Workers AI run root (ending in /ai/run) or AI Gateway Workers AI root (ending in /workers-ai), with the account and gateway IDs filled in. Do not append the model path.",
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
	"claude-bin":        "Use Set up Claude, then sign in normally with term-llm proxy run claude.",
	"agy-bin":           "Personal subscriptions only. Use Antigravity setup, then sign in with term-llm proxy run agy -- acp --login on Linux. The device receives a scoped proxy token, not Google credentials.",
	"bedrock":           "Enter https://bedrock-runtime.<region>.amazonaws.com for the region stored with the account. The proxy signs each request; only model invoke and converse calls are forwarded.",
	"opencode-go":       "One key, one grant, three protocols: Chat Completions and Responses at /, Messages at /messages/, and Gemini at /gemini/. The client still sends its own x-opencode-client and x-opencode-session headers.",
	"elevenlabs":        "The client appends /v1 speech or music paths. Put the client token in its API key field.",
	"ollama":            "Localhost is the proxy machine. Acknowledge HTTP below. No upstream key is substituted; clients still authenticate to the proxy with their client token.",
	"vllm":              "Enter your server URL. Upstream bearer authentication is optional: remove the substitution if the server has no key.",
	"openai-compatible": "Enter your server URL. Upstream bearer authentication is optional: remove the substitution if the server has no key.",
}

func presetsFromProfiles(profiles []proxyprofile.Profile) []routePreset {
	out := make([]routePreset, 0, len(profiles))
	index := make(map[string]int, len(profiles))
	for _, p := range profiles {
		// A gated profile has no verified credentialless path; offering it would
		// claim an integration the proxy cannot deliver. An account kind without
		// proxy custody support cannot be selected either.
		if p.Gate != "" || p.Account != "" && !accountKind(p.Account) {
			continue
		}
		// Keep these profiles readable for existing routes, but do not offer
		// them as presets or provision them alongside a new plan.
		switch p.ID {
		case "grok-bin", "opencode-go-messages", "opencode-go-gemini":
			continue
		}
		preset := routePreset{ID: p.ID, Label: p.Label, Upstream: p.Upstream, Auth: Auth{Header: p.Header, Prefix: p.Prefix}, Note: presetNotes[p.ID], Billing: p.Billing, WebSocket: p.WebSocket, AccountKind: p.Account, Family: p.Family, Allow: slices.Clone(p.Allow)}
		if p.ID == "opencode-go" {
			preset.Upstream, preset.Auth = "", Auth{}
			preset.Rules = []routePresetRule{
				{PathPrefix: "/", Upstream: "https://opencode.ai/zen/go/v1", Auth: Auth{Header: "Authorization", Prefix: "Bearer "}},
				{PathPrefix: "/messages/", Upstream: "https://opencode.ai/zen/go", Auth: Auth{Header: "x-api-key"}},
				{PathPrefix: "/gemini/", Upstream: "https://opencode.ai/inference/go/v1", Auth: Auth{Header: "x-goog-api-key"}},
			}
		}
		if len(preset.Rules) != 0 {
			for i := range preset.Rules {
				preset.Rules[i].Allow = slices.Clone(preset.Allow)
			}
			preset.Allow = nil
		}
		if p.Billing == "local" {
			preset.Auth = Auth{} // Default bearer client authentication, no upstream key.
		}
		if p.Provider != "" {
			preset.Usage = "auto"
		} else if p.ClassifyProvider != "" {
			preset.Usage = "off"
		}
		if p.Account != "" && nativeProviderForKind(p.Account) == nil {
			preset.Note = joinNote("Uses a trusted "+p.Account+" account held by the proxy; choose it below. No API key is stored on the route.", preset.Note)
		}
		if p.WebSocket {
			preset.Note = joinNote(preset.Note, "WebSocket upgrades are forwarded for this route.")
		}
		if p.PathSuffix != "" {
			preset.Note = joinNote(preset.Note, "Full-endpoint clients append "+p.PathSuffix+" to the route URL.")
		}
		index[p.ID] = len(out)
		out = append(out, preset)
	}
	for _, p := range profiles {
		parent, parentOffered := index[p.Parent]
		_, childOffered := index[p.ID]
		if parentOffered && childOffered {
			out[parent].Companions = append(out[parent].Companions, p.ID)
		}
	}
	return out
}

func routePresetByID(id string) (routePreset, bool) {
	for _, preset := range routePresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return routePreset{}, false
}

func providerPresetCompanions(p routePreset) []string {
	return append([]string{p.ID}, p.Companions...)
}

func (p routePreset) newRoute(provider, name, secret string) Route {
	r := Route{Name: name, Provider: provider, Profile: p.ID, Enabled: true,
		LocalPrefix: "/" + name + "/", Upstream: p.Upstream, CallerAuth: p.Auth,
		WebSocket: p.WebSocket, Allow: slices.Clone(p.Allow), Billing: p.Billing, Usage: p.Usage}
	r.SetHeaders = presetHeaders(p.Auth, secret)
	for _, child := range p.Rules {
		r.Rules = append(r.Rules, RouteRule{PathPrefix: child.PathPrefix, Upstream: child.Upstream,
			CallerAuth: child.Auth, SetHeaders: presetHeaders(child.Auth, secret), Allow: slices.Clone(child.Allow)})
	}
	return r
}

func presetHeaders(auth Auth, secret string) map[string]HeaderRule {
	if secret == "" || auth.Header == "" {
		return nil
	}
	return map[string]HeaderRule{auth.Header: {Secret: secret, Prefix: auth.Prefix}}
}

func joinNote(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " " + b
}
