package proxyprofile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/term-llm/term-llm/internal/providerspec"
	"gopkg.in/yaml.v3"
)

const maxClientProviderYAMLBytes = 64 << 10

// ForbiddenLocalProviderFields lists term-llm provider fields that must never
// be supplied by a credential proxy (secrets, transport URLs, subprocess env,
// local headers, and local enablement flags).
var forbiddenLocalProviderFields = map[string]bool{
	"api_key":           true,
	"management_key":    true,
	"access_key_id":     true,
	"secret_access_key": true,
	"session_token":     true,
	"profile":           true,
	"credentials":       true,
	"base_url":          true,
	"url":               true,
	"proxy":             true,
	"env":               true,
	"headers":           true,
	"enable_hooks":      true,
	"enabled":           true,
}

// ClientModelEntry mirrors config.ProviderModelConfig while supporting both
// plain string entries ("model-id") and structured metadata objects in YAML/JSON.
type ClientModelEntry struct {
	ID                     string   `json:"id,omitempty" yaml:"id,omitempty"`
	Alias                  string   `json:"alias,omitempty" yaml:"alias,omitempty"`
	ContextWindow          int      `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	ParseReasoning         *bool    `json:"parse_reasoning,omitempty" yaml:"parse_reasoning,omitempty"`
	IncludeReasoning       *bool    `json:"include_reasoning,omitempty" yaml:"include_reasoning,omitempty"`
	ThinkingParam          string   `json:"thinking_param,omitempty" yaml:"thinking_param,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty" yaml:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty" yaml:"default_reasoning_effort,omitempty"`
	VisionVia              string   `json:"vision_via,omitempty" yaml:"vision_via,omitempty"`
}

type clientModelEntryObject struct {
	ID                     string   `json:"id,omitempty" yaml:"id,omitempty"`
	Alias                  string   `json:"alias,omitempty" yaml:"alias,omitempty"`
	ContextWindow          int      `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	ParseReasoning         *bool    `json:"parse_reasoning,omitempty" yaml:"parse_reasoning,omitempty"`
	IncludeReasoning       *bool    `json:"include_reasoning,omitempty" yaml:"include_reasoning,omitempty"`
	ThinkingParam          string   `json:"thinking_param,omitempty" yaml:"thinking_param,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty" yaml:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty" yaml:"default_reasoning_effort,omitempty"`
	VisionVia              string   `json:"vision_via,omitempty" yaml:"vision_via,omitempty"`
}

// IsSimple reports whether this entry only specifies a model ID string.
func (m ClientModelEntry) IsSimple() bool {
	return strings.TrimSpace(m.ID) != "" &&
		strings.TrimSpace(m.Alias) == "" &&
		m.ContextWindow == 0 &&
		m.MaxOutputTokens == 0 &&
		m.ParseReasoning == nil &&
		m.IncludeReasoning == nil &&
		strings.TrimSpace(m.ThinkingParam) == "" &&
		len(m.ReasoningEfforts) == 0 &&
		strings.TrimSpace(m.DefaultReasoningEffort) == "" &&
		strings.TrimSpace(m.VisionVia) == ""
}

// DisplayName returns the alias when set, otherwise the model ID.
func (m ClientModelEntry) DisplayName() string {
	if alias := strings.TrimSpace(m.Alias); alias != "" {
		return alias
	}
	return strings.TrimSpace(m.ID)
}

func (m ClientModelEntry) MarshalYAML() (any, error) {
	if m.IsSimple() {
		return strings.TrimSpace(m.ID), nil
	}
	return clientModelEntryObject(m), nil
}

func (m *ClientModelEntry) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" && value.Tag != "" {
			return fmt.Errorf("model entry must be a string or object")
		}
		var s string
		if err := value.Decode(&s); err != nil {
			return fmt.Errorf("invalid model string: %w", err)
		}
		*m = ClientModelEntry{ID: strings.TrimSpace(s)}
		return nil
	case yaml.MappingNode:
		var raw bytes.Buffer
		enc := yaml.NewEncoder(&raw)
		if err := enc.Encode(value); err != nil {
			return err
		}
		_ = enc.Close()
		var obj clientModelEntryObject
		dec := yaml.NewDecoder(&raw)
		dec.KnownFields(true)
		if err := dec.Decode(&obj); err != nil {
			return fmt.Errorf("invalid model object: %w", err)
		}
		*m = ClientModelEntry(obj)
		return nil
	default:
		return fmt.Errorf("model entry must be a string or object")
	}
}

func (m ClientModelEntry) MarshalJSON() ([]byte, error) {
	if m.IsSimple() {
		return json.Marshal(strings.TrimSpace(m.ID))
	}
	return json.Marshal(clientModelEntryObject(m))
}

func (m *ClientModelEntry) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("empty model entry")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("invalid model string: %w", err)
		}
		*m = ClientModelEntry{ID: strings.TrimSpace(s)}
		return nil
	}
	var obj clientModelEntryObject
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("invalid model object: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return fmt.Errorf("unexpected trailing data in model object")
	}
	*m = ClientModelEntry(obj)
	return nil
}

// ClientResponsesConfig mirrors config.ResponsesConfig for proxy-discovered providers.
type ClientResponsesConfig struct {
	ReasoningMode           string                                 `json:"reasoning_mode,omitempty" yaml:"reasoning_mode,omitempty"`
	ReasoningContext        string                                 `json:"reasoning_context,omitempty" yaml:"reasoning_context,omitempty"`
	MultiAgent              *ClientResponsesMultiAgentConfig       `json:"multi_agent,omitempty" yaml:"multi_agent,omitempty"`
	ProgrammaticToolCalling *ClientResponsesProgrammaticToolConfig `json:"programmatic_tool_calling,omitempty" yaml:"programmatic_tool_calling,omitempty"`
	PromptCache             *ClientResponsesPromptCacheConfig      `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
}

type ClientResponsesMultiAgentConfig struct {
	Enabled                bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	MaxConcurrentSubagents int  `json:"max_concurrent_subagents,omitempty" yaml:"max_concurrent_subagents,omitempty"`
}

type ClientResponsesProgrammaticToolConfig struct {
	Enabled bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Tools   []string `json:"tools,omitempty" yaml:"tools,omitempty"`
}

type ClientResponsesPromptCacheConfig struct {
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	TTL  string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
}

// ClientFileUploadConfig mirrors config.FileUploadConfig for proxy-discovered providers.
type ClientFileUploadConfig struct {
	NativeMimeTypes    []string `json:"native_mime_types,omitempty" yaml:"native_mime_types,omitempty"`
	MaxNativeBytes     int64    `json:"max_native_bytes,omitempty" yaml:"max_native_bytes,omitempty"`
	TextEmbedMimeTypes []string `json:"text_embed_mime_types,omitempty" yaml:"text_embed_mime_types,omitempty"`
	MaxTextEmbedBytes  int64    `json:"max_text_embed_bytes,omitempty" yaml:"max_text_embed_bytes,omitempty"`
}

// ClientProviderConfig is the strict allowlist of declarative inference fields
// a credential proxy may advertise for a provider. It deliberately omits
// credentials, URLs, headers, env vars, hooks, and local enablement flags.
type ClientProviderConfig struct {
	Type                      providerspec.Type       `json:"type,omitempty" yaml:"type,omitempty"`
	Model                     string                  `json:"model,omitempty" yaml:"model,omitempty"`
	FastModel                 string                  `json:"fast_model,omitempty" yaml:"fast_model,omitempty"`
	FastProvider              string                  `json:"fast_provider,omitempty" yaml:"fast_provider,omitempty"`
	ServiceTier               string                  `json:"service_tier,omitempty" yaml:"service_tier,omitempty"`
	AllowedModels             []string                `json:"allowed_models,omitempty" yaml:"allowed_models,omitempty"`
	Models                    []ClientModelEntry      `json:"models,omitempty" yaml:"models,omitempty"`
	Reasoning                 string                  `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	ThinkingSummaries         *bool                   `json:"thinking_summaries,omitempty" yaml:"thinking_summaries,omitempty"`
	UseWebSocket              *bool                   `json:"use_websocket,omitempty" yaml:"use_websocket,omitempty"`
	UsePreviousResponseID     *bool                   `json:"use_previous_response_id,omitempty" yaml:"use_previous_response_id,omitempty"`
	RequestEncryptedReasoning *bool                   `json:"request_encrypted_reasoning,omitempty" yaml:"request_encrypted_reasoning,omitempty"`
	Responses                 *ClientResponsesConfig  `json:"responses,omitempty" yaml:"responses,omitempty"`
	FileUpload                *ClientFileUploadConfig `json:"file_upload,omitempty" yaml:"file_upload,omitempty"`
	VisionVia                 string                  `json:"vision_via,omitempty" yaml:"vision_via,omitempty"`
	UseNativeSearch           *bool                   `json:"use_native_search,omitempty" yaml:"use_native_search,omitempty"`
	ContextWindow             int                     `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	MaxOutputTokens           int                     `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	NoStreamOptions           *bool                   `json:"no_stream_options,omitempty" yaml:"no_stream_options,omitempty"`
	VLLMThinkingParam         string                  `json:"vllm_thinking_param,omitempty" yaml:"vllm_thinking_param,omitempty"`
	ParseReasoning            *bool                   `json:"parse_reasoning,omitempty" yaml:"parse_reasoning,omitempty"`
	IncludeReasoning          *bool                   `json:"include_reasoning,omitempty" yaml:"include_reasoning,omitempty"`
	ThinkingParam             string                  `json:"thinking_param,omitempty" yaml:"thinking_param,omitempty"`
	AppURL                    string                  `json:"app_url,omitempty" yaml:"app_url,omitempty"`
	AppTitle                  string                  `json:"app_title,omitempty" yaml:"app_title,omitempty"`
	Region                    string                  `json:"region,omitempty" yaml:"region,omitempty"`
	ModelMap                  map[string]string       `json:"model_map,omitempty" yaml:"model_map,omitempty"`
	Think                     *bool                   `json:"think,omitempty" yaml:"think,omitempty"`
	ThinkLevel                string                  `json:"think_level,omitempty" yaml:"think_level,omitempty"`
	TopK                      *int                    `json:"top_k,omitempty" yaml:"top_k,omitempty"`
	MinP                      *float64                `json:"min_p,omitempty" yaml:"min_p,omitempty"`
	PresencePenalty           *float64                `json:"presence_penalty,omitempty" yaml:"presence_penalty,omitempty"`
	NumCtx                    *int                    `json:"num_ctx,omitempty" yaml:"num_ctx,omitempty"`
	NumPredict                *int                    `json:"num_predict,omitempty" yaml:"num_predict,omitempty"`
}

// IsZero reports whether no client config fields are set.
func (c ClientProviderConfig) IsZero() bool {
	return reflect.DeepEqual(c, ClientProviderConfig{})
}

// Validate checks bounds and semantic invariants for a proxy-advertised config.
func (c ClientProviderConfig) Validate() error {
	if err := c.validateTypeAndReasoning(); err != nil {
		return err
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	return c.validateLimits()
}

func (c ClientProviderConfig) validateTypeAndReasoning() error {
	if c.Type != "" {
		profile, ok := ForProvider(c.Type)
		if !ok {
			return fmt.Errorf("unsupported provider type %q", c.Type)
		}
		if profile.Gate != "" {
			return fmt.Errorf("provider type %q cannot be advertised through the proxy yet", c.Type)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.Reasoning)) {
	case "", "auto", "enabled", "disabled":
		return nil
	default:
		return fmt.Errorf("reasoning must be auto, enabled, or disabled")
	}
}

func (c ClientProviderConfig) validateModels() error {
	if len(c.AllowedModels) > 256 {
		return fmt.Errorf("at most 256 allowed_models entries")
	}
	for _, model := range c.AllowedModels {
		if model == "" || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.ContainsAny(model, "*?") {
			return fmt.Errorf("invalid allowed_models entry %q; use literal model names without whitespace or wildcards", model)
		}
	}
	if len(c.Models) > 256 {
		return fmt.Errorf("at most 256 models entries")
	}
	for _, entry := range c.Models {
		if err := entry.validate(); err != nil {
			return err
		}
	}
	if len(c.ModelMap) > 256 {
		return fmt.Errorf("at most 256 model_map entries")
	}
	for k, v := range c.ModelMap {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return fmt.Errorf("model_map keys and values must be non-empty")
		}
	}
	return nil
}

func (c ClientProviderConfig) validateLimits() error {
	if c.ContextWindow < 0 || c.MaxOutputTokens < 0 {
		return fmt.Errorf("context_window and max_output_tokens must not be negative")
	}
	if c.FileUpload != nil {
		if c.FileUpload.MaxNativeBytes < 0 || c.FileUpload.MaxTextEmbedBytes < 0 {
			return fmt.Errorf("file_upload byte limits must not be negative")
		}
		if len(c.FileUpload.NativeMimeTypes) > 64 || len(c.FileUpload.TextEmbedMimeTypes) > 64 {
			return fmt.Errorf("file_upload MIME type lists may have at most 64 entries")
		}
	}
	if c.Responses != nil && c.Responses.MultiAgent != nil && c.Responses.MultiAgent.MaxConcurrentSubagents < 0 {
		return fmt.Errorf("responses.multi_agent.max_concurrent_subagents must not be negative")
	}
	if c.TopK != nil && *c.TopK < 0 {
		return fmt.Errorf("top_k must not be negative")
	}
	if c.MinP != nil && (*c.MinP < 0 || *c.MinP > 1) {
		return fmt.Errorf("min_p must be between 0 and 1")
	}
	if c.NumCtx != nil && *c.NumCtx < 0 {
		return fmt.Errorf("num_ctx must not be negative")
	}
	if c.NumPredict != nil && *c.NumPredict < -1 {
		return fmt.Errorf("num_predict must be -1 or greater")
	}
	return nil
}

func (m ClientModelEntry) validate() error {
	if strings.TrimSpace(m.ID) == "" && strings.TrimSpace(m.Alias) == "" {
		return fmt.Errorf("each models entry must specify id or alias")
	}
	if m.ContextWindow < 0 || m.MaxOutputTokens < 0 {
		return fmt.Errorf("model %q context_window and max_output_tokens must not be negative", m.DisplayName())
	}
	if len(m.ReasoningEfforts) > 32 {
		return fmt.Errorf("model %q has too many reasoning_efforts entries", m.DisplayName())
	}
	defaultEffort := strings.ToLower(strings.TrimSpace(m.DefaultReasoningEffort))
	if defaultEffort == "" {
		return nil
	}
	for _, effort := range m.ReasoningEfforts {
		if strings.EqualFold(strings.TrimSpace(effort), defaultEffort) {
			return nil
		}
	}
	return fmt.Errorf("model %q default_reasoning_effort %q is not listed in reasoning_efforts", m.DisplayName(), m.DefaultReasoningEffort)
}

// ParseClientProviderYAML strictly decodes and validates a single provider's
// client config YAML. Forbidden local-only fields receive an explicit message.
func ParseClientProviderYAML(data []byte) (ClientProviderConfig, error) {
	var cfg ClientProviderConfig
	if len(data) > maxClientProviderYAMLBytes {
		return cfg, fmt.Errorf("provider config YAML exceeds 64 KiB")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return cfg, fmt.Errorf("invalid provider config YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return cfg, fmt.Errorf("provider config YAML must be a mapping of provider fields")
	}
	if err := checkForbiddenYAMLFields(doc.Content[0]); err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid provider config YAML: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return cfg, fmt.Errorf("provider config YAML must contain one document")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func checkForbiddenYAMLFields(mapping *yaml.Node) error {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := strings.TrimSpace(mapping.Content[i].Value)
		if forbiddenLocalProviderFields[key] {
			return fmt.Errorf("field %q is not allowed in proxy provider config (secrets, endpoints, headers, env, and enablement are managed by the proxy route)", key)
		}
	}
	return nil
}

// MarshalClientProviderYAML renders a ClientProviderConfig as canonical 2-space YAML.
func MarshalClientProviderYAML(cfg ClientProviderConfig) (string, error) {
	if cfg.IsZero() {
		return "", nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// ParsedTermLLMProvider holds the result of importing a pasted term-llm
// provider YAML block (bare mapping, "<name>: { ... }", or "providers: { <name>: { ... } }").
type ParsedTermLLMProvider struct {
	Name       string               `json:"name,omitempty"`
	Upstream   string               `json:"upstream,omitempty"`
	Config     ClientProviderConfig `json:"config"`
	ConfigYAML string               `json:"config_yaml"`
	Stripped   []string             `json:"stripped,omitempty"`
}

var allowedProviderMappingKeys = map[string]bool{
	"responses":   true,
	"file_upload": true,
	"model_map":   true,
	"headers":     true,
	"env":         true,
	"proxy":       true,
}

// ImportTermLLMProviderYAML parses a pasted term-llm provider YAML snippet,
// extracts any provider name and base_url, strips local-only secret/transport
// fields without evaluating them, and validates the remaining ClientProviderConfig.
func ImportTermLLMProviderYAML(data []byte) (ParsedTermLLMProvider, error) {
	var out ParsedTermLLMProvider
	if len(data) > maxClientProviderYAMLBytes {
		return out, fmt.Errorf("pasted YAML exceeds 64 KiB")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, fmt.Errorf("paste a term-llm provider YAML block first")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return out, fmt.Errorf("invalid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return out, fmt.Errorf("expected a YAML mapping (either provider fields or '<name>:' block)")
	}
	root := doc.Content[0]
	mapping, name, err := unwrapProviderMapping(root)
	if err != nil {
		return out, err
	}
	out.Name = name

	cleaned := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var stripped []string
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		kNode, vNode := mapping.Content[i], mapping.Content[i+1]
		key := strings.TrimSpace(kNode.Value)
		if key == "base_url" && vNode.Kind == yaml.ScalarNode {
			out.Upstream = strings.TrimSpace(vNode.Value)
		}
		if forbiddenLocalProviderFields[key] {
			stripped = append(stripped, key)
			continue
		}
		cleaned.Content = append(cleaned.Content, kNode, vNode)
	}
	sort.Strings(stripped)
	out.Stripped = stripped

	if len(cleaned.Content) > 0 {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(cleaned); err != nil {
			return out, err
		}
		_ = enc.Close()
		cfg, err := ParseClientProviderYAML(buf.Bytes())
		if err != nil {
			return out, err
		}
		out.Config = cfg
		rendered, err := MarshalClientProviderYAML(cfg)
		if err != nil {
			return out, err
		}
		out.ConfigYAML = rendered
	}
	return out, nil
}

func unwrapProviderMapping(root *yaml.Node) (*yaml.Node, string, error) {
	if len(root.Content) == 2 && root.Content[1].Kind == yaml.MappingNode {
		topKey := strings.TrimSpace(root.Content[0].Value)
		if topKey == "providers" {
			inner := root.Content[1]
			if len(inner.Content) != 2 || inner.Content[1].Kind != yaml.MappingNode {
				return nil, "", fmt.Errorf("providers block must contain exactly one provider entry")
			}
			return inner.Content[1], strings.TrimSpace(inner.Content[0].Value), nil
		}
		if !allowedProviderMappingKeys[topKey] {
			return root.Content[1], topKey, nil
		}
	}
	return root, "", nil
}
