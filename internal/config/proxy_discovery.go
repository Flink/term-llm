package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

const (
	proxyDiscoveryTimeout  = 10 * time.Second
	proxyDiscoveryMaxBytes = 1 << 20
)

type proxyDiscoveredRoute struct {
	Name        string `json:"name"`
	LocalPrefix string `json:"local_prefix"`
	WebSocket   bool   `json:"websocket"`
}

type proxyDiscoveredProvider struct {
	ID          string                             `json:"id"`
	LocalPrefix string                             `json:"local_prefix,omitempty"`
	WebSocket   bool                               `json:"websocket"`
	Routes      []proxyDiscoveredRoute             `json:"routes"`
	Config      *proxyprofile.ClientProviderConfig `json:"config,omitempty"`
}

type proxyDiscoveryCatalog struct {
	Revision  int64
	Providers map[string]proxyDiscoveredProvider
}

type proxyDiscoveryPayload struct {
	Revision  int64                     `json:"revision"`
	Providers []proxyDiscoveredProvider `json:"providers"`
}

type proxyDiscoveryKey struct {
	root      string
	tokenHash string
}

type proxyDiscoveryFlight struct {
	done    chan struct{}
	catalog *proxyDiscoveryCatalog
	err     error
}

var proxyDiscoveryState = struct {
	mu       sync.Mutex
	cached   map[proxyDiscoveryKey]*proxyDiscoveryCatalog
	inflight map[proxyDiscoveryKey]*proxyDiscoveryFlight
}{
	cached:   make(map[proxyDiscoveryKey]*proxyDiscoveryCatalog),
	inflight: make(map[proxyDiscoveryKey]*proxyDiscoveryFlight),
}

var fetchProxyProviders = fetchProxyProvidersHTTP

func proxyDiscoveryCacheKey(root, token string) proxyDiscoveryKey {
	sum := sha256.Sum256([]byte(token))
	return proxyDiscoveryKey{root: root, tokenHash: hex.EncodeToString(sum[:])}
}

// discoverProxyProviders fetches GET /_proxy/client/v1/providers at most once
// per process for a given (proxyRoot, token) pair. No TTL is used so an active
// process/conversation is never silently reconfigured mid-flight.
func discoverProxyProviders(root, token string) (*proxyDiscoveryCatalog, error) {
	key := proxyDiscoveryCacheKey(root, token)
	proxyDiscoveryState.mu.Lock()
	if cat, ok := proxyDiscoveryState.cached[key]; ok {
		proxyDiscoveryState.mu.Unlock()
		return cat, nil
	}
	if flight, ok := proxyDiscoveryState.inflight[key]; ok {
		proxyDiscoveryState.mu.Unlock()
		<-flight.done
		return flight.catalog, flight.err
	}
	flight := &proxyDiscoveryFlight{done: make(chan struct{})}
	proxyDiscoveryState.inflight[key] = flight
	fetcher := fetchProxyProviders
	proxyDiscoveryState.mu.Unlock()

	cat, err := fetcher(root, token)

	proxyDiscoveryState.mu.Lock()
	delete(proxyDiscoveryState.inflight, key)
	if err == nil && cat != nil {
		proxyDiscoveryState.cached[key] = cat
	}
	flight.catalog, flight.err = cat, err
	close(flight.done)
	proxyDiscoveryState.mu.Unlock()
	return cat, err
}

func resetProxyDiscoveryCache() {
	proxyDiscoveryState.mu.Lock()
	proxyDiscoveryState.cached = make(map[proxyDiscoveryKey]*proxyDiscoveryCatalog)
	proxyDiscoveryState.inflight = make(map[proxyDiscoveryKey]*proxyDiscoveryFlight)
	proxyDiscoveryState.mu.Unlock()
}

func fetchProxyProvidersHTTP(root, token string) (*proxyDiscoveryCatalog, error) {
	endpoint := strings.TrimRight(root, "/") + "/_proxy/client/v1/providers"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("proxy provider discovery request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout: proxyDiscoveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy provider discovery failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("proxy provider discovery unauthorized (check proxy.token)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy provider discovery failed: HTTP %d", resp.StatusCode)
	}

	var payload proxyDiscoveryPayload
	dec := json.NewDecoder(io.LimitReader(resp.Body, proxyDiscoveryMaxBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("proxy provider discovery returned invalid JSON: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("proxy provider discovery returned trailing data")
	}
	return validateDiscoveryPayload(payload)
}

func validateDiscoveryPayload(payload proxyDiscoveryPayload) (*proxyDiscoveryCatalog, error) {
	byID := make(map[string]proxyDiscoveredProvider, len(payload.Providers))
	for _, p := range payload.Providers {
		if !proxyRouteName.MatchString(p.ID) {
			return nil, fmt.Errorf("proxy provider discovery returned invalid provider id %q", p.ID)
		}
		if p.LocalPrefix != "" && !validProxyLocalPrefix(p.LocalPrefix) {
			return nil, fmt.Errorf("proxy provider %q returned unsafe local_prefix %q", p.ID, p.LocalPrefix)
		}
		if p.Config != nil {
			if err := p.Config.Validate(); err != nil {
				return nil, fmt.Errorf("proxy provider %q returned invalid client config: %w", p.ID, err)
			}
		}
		byID[p.ID] = p
	}
	return &proxyDiscoveryCatalog{Revision: payload.Revision, Providers: byID}, nil
}

func validProxyLocalPrefix(prefix string) bool {
	if len(prefix) > 512 || !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || prefix == "/" {
		return false
	}
	if strings.Contains(prefix, "//") || strings.ContainsAny(prefix, "%\\\r\n\t ") {
		return false
	}
	for _, seg := range strings.Split(prefix, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func isReservedProxyRouteID(id string) bool {
	for _, p := range proxyprofile.All() {
		if p.ID == id && (p.Provider == "" || p.ID != string(p.Provider)) {
			return true
		}
	}
	return false
}

func lookupDiscoveredProvider(catalog *proxyDiscoveryCatalog, name, canonicalID string) (proxyDiscoveredProvider, bool) {
	if catalog == nil {
		return proxyDiscoveredProvider{}, false
	}
	if p, ok := catalog.Providers[name]; ok {
		return p, true
	}
	if canonicalID != "" && canonicalID != name {
		if p, ok := catalog.Providers[canonicalID]; ok {
			return p, true
		}
	}
	return proxyDiscoveredProvider{}, false
}

func mergeDiscoveredProviderConfig(name string, pc ProviderConfig, discovered proxyprofile.ClientProviderConfig, file *fileSettings) (ProviderConfig, error) {
	localSet := func(field string, nonZero bool) bool {
		if file != nil {
			return file.has("providers." + name + "." + field)
		}
		return !pc.FromDefaults && nonZero
	}
	if discovered.Type != "" {
		if localSet("type", pc.Type != "") && pc.Type != discovered.Type {
			return pc, fmt.Errorf("providers.%s.type (%s) conflicts with proxy-discovered type (%s)", name, pc.Type, discovered.Type)
		}
		pc.Type = discovered.Type
	}
	mergeDiscoveredCoreFields(&pc, discovered, localSet)
	mergeDiscoveredProtocolFields(&pc, discovered, localSet)
	mergeDiscoveredOllamaOptions(&pc, discovered, localSet)
	return pc, nil
}

func mergeDiscoveredCoreFields(pc *ProviderConfig, discovered proxyprofile.ClientProviderConfig, localSet func(string, bool) bool) {
	setString(&pc.Model, discovered.Model, "model", localSet)
	setString(&pc.FastModel, discovered.FastModel, "fast_model", localSet)
	setString(&pc.FastProvider, discovered.FastProvider, "fast_provider", localSet)
	setString(&pc.ServiceTier, discovered.ServiceTier, "service_tier", localSet)
	setString(&pc.Reasoning, discovered.Reasoning, "reasoning", localSet)
	setString(&pc.VisionVia, discovered.VisionVia, "vision_via", localSet)
	if len(discovered.AllowedModels) > 0 && !localSet("allowed_models", len(pc.AllowedModels) > 0) {
		pc.AllowedModels = append([]string(nil), discovered.AllowedModels...)
	}
	if len(discovered.Models) > 0 && !localSet("models", len(pc.Models) > 0 || len(pc.ModelConfigs) > 0) {
		pc.Models, pc.ModelConfigs = convertDiscoveredModels(discovered.Models)
	}
	if discovered.ContextWindow > 0 && !localSet("context_window", pc.ContextWindow != 0) {
		pc.ContextWindow = discovered.ContextWindow
	}
	if discovered.MaxOutputTokens > 0 && !localSet("max_output_tokens", pc.MaxOutputTokens != 0) {
		pc.MaxOutputTokens = discovered.MaxOutputTokens
	}
}

func mergeDiscoveredProtocolFields(pc *ProviderConfig, discovered proxyprofile.ClientProviderConfig, localSet func(string, bool) bool) {
	setBoolPtr(&pc.ThinkingSummaries, discovered.ThinkingSummaries, "thinking_summaries", localSet)
	setBoolVal(&pc.UseWebSocket, discovered.UseWebSocket, "use_websocket", localSet)
	setBoolPtr(&pc.UsePreviousResponseID, discovered.UsePreviousResponseID, "use_previous_response_id", localSet)
	setBoolPtr(&pc.RequestEncryptedReasoning, discovered.RequestEncryptedReasoning, "request_encrypted_reasoning", localSet)
	setBoolPtr(&pc.UseNativeSearch, discovered.UseNativeSearch, "use_native_search", localSet)
	setBoolVal(&pc.NoStreamOptions, discovered.NoStreamOptions, "no_stream_options", localSet)
	setBoolPtr(&pc.ParseReasoning, discovered.ParseReasoning, "parse_reasoning", localSet)
	setBoolPtr(&pc.IncludeReasoning, discovered.IncludeReasoning, "include_reasoning", localSet)
	setString(&pc.VLLMThinkingParam, discovered.VLLMThinkingParam, "vllm_thinking_param", localSet)
	setString(&pc.ThinkingParam, discovered.ThinkingParam, "thinking_param", localSet)
	setString(&pc.AppURL, discovered.AppURL, "app_url", localSet)
	setString(&pc.AppTitle, discovered.AppTitle, "app_title", localSet)
	setString(&pc.Region, discovered.Region, "region", localSet)
	if discovered.Responses != nil && !localSet("responses", !responsesConfigEmpty(pc.Responses)) {
		pc.Responses = convertDiscoveredResponses(*discovered.Responses)
	}
	if discovered.FileUpload != nil && !localSet("file_upload", pc.FileUpload != nil) {
		pc.FileUpload = convertDiscoveredFileUpload(discovered.FileUpload)
	}
	if len(discovered.ModelMap) > 0 && !localSet("model_map", len(pc.ModelMap) > 0) {
		pc.ModelMap = make(map[string]string, len(discovered.ModelMap))
		for k, v := range discovered.ModelMap {
			pc.ModelMap[k] = v
		}
	}
}

func setString(dst *string, src, field string, localSet func(string, bool) bool) {
	if src != "" && !localSet(field, *dst != "") {
		*dst = src
	}
}

func setBoolPtr(dst **bool, src *bool, field string, localSet func(string, bool) bool) {
	if src != nil && !localSet(field, *dst != nil) {
		v := *src
		*dst = &v
	}
}

func setBoolVal(dst *bool, src *bool, field string, localSet func(string, bool) bool) {
	if src != nil && !localSet(field, *dst) {
		*dst = *src
	}
}

func mergeDiscoveredOllamaOptions(pc *ProviderConfig, discovered proxyprofile.ClientProviderConfig, localSet func(string, bool) bool) {
	if discovered.Think != nil && !localSet("think", pc.Think != nil) {
		v := *discovered.Think
		pc.Think = &v
	}
	if discovered.ThinkLevel != "" && !localSet("think_level", pc.ThinkLevel != "") {
		pc.ThinkLevel = discovered.ThinkLevel
	}
	if discovered.TopK != nil && !localSet("top_k", pc.TopK != nil) {
		v := *discovered.TopK
		pc.TopK = &v
	}
	if discovered.MinP != nil && !localSet("min_p", pc.MinP != nil) {
		v := *discovered.MinP
		pc.MinP = &v
	}
	if discovered.PresencePenalty != nil && !localSet("presence_penalty", pc.PresencePenalty != nil) {
		v := *discovered.PresencePenalty
		pc.PresencePenalty = &v
	}
	if discovered.NumCtx != nil && !localSet("num_ctx", pc.NumCtx != nil) {
		v := *discovered.NumCtx
		pc.NumCtx = &v
	}
	if discovered.NumPredict != nil && !localSet("num_predict", pc.NumPredict != nil) {
		v := *discovered.NumPredict
		pc.NumPredict = &v
	}
}

func convertDiscoveredModels(entries []proxyprofile.ClientModelEntry) ([]string, []ProviderModelConfig) {
	names := make([]string, 0, len(entries))
	var configs []ProviderModelConfig
	for _, entry := range entries {
		if display := entry.DisplayName(); display != "" {
			names = append(names, display)
		}
		if entry.IsSimple() {
			continue
		}
		mc := ProviderModelConfig{
			ID:                     strings.TrimSpace(entry.ID),
			Alias:                  strings.TrimSpace(entry.Alias),
			ContextWindow:          entry.ContextWindow,
			MaxOutputTokens:        entry.MaxOutputTokens,
			ThinkingParam:          strings.TrimSpace(entry.ThinkingParam),
			ReasoningEfforts:       append([]string(nil), entry.ReasoningEfforts...),
			DefaultReasoningEffort: strings.TrimSpace(entry.DefaultReasoningEffort),
			VisionVia:              strings.TrimSpace(entry.VisionVia),
		}
		if entry.ParseReasoning != nil {
			v := *entry.ParseReasoning
			mc.ParseReasoning = &v
		}
		if entry.IncludeReasoning != nil {
			v := *entry.IncludeReasoning
			mc.IncludeReasoning = &v
		}
		configs = append(configs, mc)
	}
	return names, configs
}

func convertDiscoveredResponses(r proxyprofile.ClientResponsesConfig) ResponsesConfig {
	out := ResponsesConfig{
		ReasoningMode:    r.ReasoningMode,
		ReasoningContext: r.ReasoningContext,
	}
	if r.MultiAgent != nil {
		out.MultiAgent = ResponsesMultiAgentConfig{
			Enabled:                r.MultiAgent.Enabled,
			MaxConcurrentSubagents: r.MultiAgent.MaxConcurrentSubagents,
		}
	}
	if r.ProgrammaticToolCalling != nil {
		out.ProgrammaticToolCalling = ResponsesProgrammaticToolsConfig{
			Enabled: r.ProgrammaticToolCalling.Enabled,
			Tools:   append([]string(nil), r.ProgrammaticToolCalling.Tools...),
		}
	}
	if r.PromptCache != nil {
		out.PromptCache = ResponsesPromptCacheConfig{
			Mode: r.PromptCache.Mode,
			TTL:  r.PromptCache.TTL,
		}
	}
	return out
}

func responsesConfigEmpty(r ResponsesConfig) bool {
	return r.ReasoningMode == "" &&
		r.ReasoningContext == "" &&
		!r.MultiAgent.Enabled &&
		r.MultiAgent.MaxConcurrentSubagents == 0 &&
		!r.ProgrammaticToolCalling.Enabled &&
		len(r.ProgrammaticToolCalling.Tools) == 0 &&
		r.PromptCache.Mode == "" &&
		r.PromptCache.TTL == ""
}

func convertDiscoveredFileUpload(f *proxyprofile.ClientFileUploadConfig) *FileUploadConfig {
	if f == nil {
		return nil
	}
	return &FileUploadConfig{
		NativeMimeTypes:    append([]string(nil), f.NativeMimeTypes...),
		MaxNativeBytes:     f.MaxNativeBytes,
		TextEmbedMimeTypes: append([]string(nil), f.TextEmbedMimeTypes...),
		MaxTextEmbedBytes:  f.MaxTextEmbedBytes,
	}
}
