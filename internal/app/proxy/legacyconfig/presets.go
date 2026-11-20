package legacyconfig

// routePresets is the v8 picker table, frozen independently of live profiles.
// Gated profiles and OpenCode's separately offered client protocols were not
// legacy presets. OpenCode Go's three-rule preset and Ollama's keyless auth
// are the server's v8 overrides of the profile table.
var routePresets = []routePreset{
	{ID: "anthropic", Label: "Anthropic API", Upstream: "https://api.anthropic.com", Auth: auth{Header: "x-api-key"}, Inference: true},
	{ID: "openai", Label: "OpenAI API", Upstream: "https://api.openai.com/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "chatgpt", Label: "ChatGPT plan", Upstream: "https://chatgpt.com/backend-api/codex", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "chatgpt", Companions: []string{"chatgpt-backend", "chatgpt-openai"}, Inference: true},
	{ID: "chatgpt-backend", Label: "ChatGPT plan usage and dictation", Upstream: "https://chatgpt.com/backend-api", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "chatgpt"},
	{ID: "chatgpt-openai", Label: "ChatGPT OAuth embeddings and live (billing unspecified)", Upstream: "https://api.openai.com/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "chatgpt"},
	{ID: "grok", Label: "Grok plan", Upstream: "https://cli-chat-proxy.grok.com/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "grok", Companions: []string{"grok-bin"}, Inference: true},
	{ID: "copilot", Label: "GitHub Copilot plan", Upstream: "https://api.githubcopilot.com", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "copilot", Inference: true},
	{ID: "gemini", Label: "Google Gemini", Upstream: "https://generativelanguage.googleapis.com/v1beta", Auth: auth{Header: "x-goog-api-key"}, Companions: []string{"gemini-live"}, Inference: true},
	{ID: "gemini-live", Label: "Gemini Live", Upstream: "https://generativelanguage.googleapis.com", Auth: auth{Header: "x-goog-api-key"}},
	{ID: "openrouter", Label: "OpenRouter", Upstream: "https://openrouter.ai/api/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "xai", Label: "xAI API", Upstream: "https://api.x.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "venice", Label: "Venice", Upstream: "https://api.venice.ai/api/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "nearai", Label: "NEAR AI", Upstream: "https://cloud-api.near.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "sambanova", Label: "SambaNova", Upstream: "https://api.sambanova.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "obscurify", Label: "Obscurify", Upstream: "https://obscurify.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "zen", Label: "OpenCode Zen", Upstream: "https://opencode.ai/zen/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "opencode-go", Label: "OpenCode Go", Inference: true, Rules: []routePresetRule{
		{PathPrefix: "/", Upstream: "https://opencode.ai/zen/go/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}},
		{PathPrefix: "/messages/", Upstream: "https://opencode.ai/zen/go", Auth: auth{Header: "x-api-key"}},
		{PathPrefix: "/gemini/", Upstream: "https://opencode.ai/inference/go/v1", Auth: auth{Header: "x-goog-api-key"}},
	}},
	{ID: "bedrock", Label: "AWS Bedrock", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "aws-sigv4", Inference: true},
	{ID: "ollama", Label: "Ollama", Upstream: "http://127.0.0.1:11434", Inference: true},
	{ID: "vllm", Label: "vLLM", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "openai-compatible", Label: "OpenAI-compatible server", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
	{ID: "claude-code", Label: "Claude Code plan", Upstream: "https://api.anthropic.com", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "claude-code", Inference: true},
	{ID: "grok-bin", Label: "Grok CLI plan", Upstream: "https://cli-chat-proxy.grok.com/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "grok", Inference: true},
	{ID: "agy-bin", Label: "Antigravity personal subscription", Upstream: "https://daily-cloudcode-pa.googleapis.com", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, AccountKind: "agy-bin", Inference: true},
	{ID: "elevenlabs", Label: "ElevenLabs", Upstream: "https://api.elevenlabs.io", Auth: auth{Header: "xi-api-key"}, Family: "media"},
	{ID: "flux", Label: "Flux", Upstream: "https://api.bfl.ai/v1", Auth: auth{Header: "x-key"}, Family: "media"},
	{ID: "jina", Label: "Jina embeddings", Upstream: "https://api.jina.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Family: "embeddings"},
	{ID: "voyage", Label: "Voyage embeddings", Upstream: "https://api.voyageai.com/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Family: "embeddings"},
	{ID: "mistral", Label: "Mistral", Upstream: "https://api.mistral.ai/v1", Auth: auth{Header: "Authorization", Prefix: "Bearer "}, Inference: true},
}

// These inference profiles were not offered in the v8 picker. They can only
// be found by exact name and matching account kind, never by the matcher.
var gatedProfiles = []routePreset{
	{ID: "cursor-bin", AccountKind: "cursor", Inference: true},
	{ID: "vibe-bin", Inference: true},
}

// v8 built-in client names are used as a secondary provider anchor.
func builtinName(name string) bool {
	switch name {
	case "anthropic", "openai", "chatgpt", "grok", "gemini", "openrouter", "xai", "venice", "nearai", "sambanova", "obscurify", "zen", "opencode-go", "copilot", "claude-bin", "grok-bin", "cursor-bin", "agy-bin", "vibe-bin", "ollama", "vllm", "bedrock":
		return true
	}
	return false
}
