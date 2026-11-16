package proxy

// routePreset only supplies editable creation-time defaults. Forwarding and
// validation remain provider-independent; no credentials or client state live here.
type routePreset struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Upstream string `json:"upstream"`
	Auth     Auth   `json:"auth"`
	Note     string `json:"note,omitempty"`
}

var bearerPresetAuth = Auth{Header: "Authorization", Prefix: "Bearer "}

var routePresets = []routePreset{
	{"openai", "OpenAI API", "https://api.openai.com/v1", bearerPresetAuth, "Use HTTP, not WebSocket transport. Set your client's base URL to this route's local prefix, without adding /v1 again."},
	{"anthropic", "Anthropic API", "https://api.anthropic.com", Auth{Header: "x-api-key"}, "The client sends anthropic-version and beta headers unchanged. Put the client token in the client's API key field."},
	{"gemini", "Google Gemini", "https://generativelanguage.googleapis.com/v1beta", Auth{Header: "x-goog-api-key"}, "Use a header-based API key, not ?key=. The local route replaces the /v1beta API root."},
	{"openrouter", "OpenRouter", "https://openrouter.ai/api/v1", bearerPresetAuth, ""},
	{"xai", "xAI API", "https://api.x.ai/v1", bearerPresetAuth, "API key access, not Grok subscription OAuth."},
	{"venice", "Venice", "https://api.venice.ai/api/v1", bearerPresetAuth, ""},
	{"nearai", "NEAR AI", "https://cloud-api.near.ai/v1", bearerPresetAuth, ""},
	{"sambanova", "SambaNova", "https://api.sambanova.ai/v1", bearerPresetAuth, ""},
	{"obscurify", "Obscurify", "https://obscurify.ai/v1", bearerPresetAuth, ""},
	{"zen", "OpenCode Zen", "https://opencode.ai/zen/v1", bearerPresetAuth, ""},
	{"opencode-go", "OpenCode Go · Chat / Responses", "https://opencode.ai/zen/go/v1", bearerPresetAuth, "Use a compatible Chat/Responses client, not the dynamic opencode-go integration. The client must send x-opencode-client and x-opencode-session."},
	{"opencode-go-messages", "OpenCode Go · Messages", "https://opencode.ai/zen/go", Auth{Header: "x-api-key"}, "Use an Anthropic-compatible client with x-opencode-client and x-opencode-session. Not the dynamic opencode-go integration or Gemini protocol."},
	{"elevenlabs", "ElevenLabs", "https://api.elevenlabs.io", Auth{Header: "xi-api-key"}, "The client appends /v1 speech or music paths. Put the client token in its API key field."},
	{"jina", "Jina embeddings", "https://api.jina.ai/v1", bearerPresetAuth, ""},
	{"voyage", "Voyage AI embeddings", "https://api.voyageai.com/v1", bearerPresetAuth, ""},
	{"mistral", "Mistral transcription", "https://api.mistral.ai/v1", bearerPresetAuth, ""},
	{"ollama", "Ollama · local server", "http://127.0.0.1:11434", Auth{}, "Localhost is on the proxy machine. Acknowledge HTTP below. Use a client that can send the client token; the native term-llm Ollama client cannot."},
	{"vllm", "vLLM · your server", "", bearerPresetAuth, "Enter your server URL. Upstream bearer authentication is optional: remove the substitution if the server has no key."},
}
