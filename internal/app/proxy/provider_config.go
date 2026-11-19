package proxy

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

type clientProviderRoute struct {
	Name        string `json:"name"`
	LocalPrefix string `json:"local_prefix"`
	WebSocket   bool   `json:"websocket"`
}

type clientProviderInfo struct {
	ID          string                             `json:"id"`
	LocalPrefix string                             `json:"local_prefix,omitempty"`
	WebSocket   bool                               `json:"websocket"`
	Routes      []clientProviderRoute              `json:"routes"`
	Config      *proxyprofile.ClientProviderConfig `json:"config,omitempty"`
}

func validateProviderConfigs(configs map[string]proxyprofile.ClientProviderConfig, routes []Route) (map[string]proxyprofile.ClientProviderConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if len(configs) > 128 {
		return nil, fmt.Errorf("at most 128 provider configs")
	}
	known := validProviderConfigIDs(routes)
	cleaned := make(map[string]proxyprofile.ClientProviderConfig, len(configs))
	for id, cfg := range configs {
		if !slug.MatchString(id) {
			return nil, fmt.Errorf("provider config key %q must be a slug of at most 64 characters", id)
		}
		if !known[id] {
			return nil, fmt.Errorf("provider config %q does not match any configured route or provider group", id)
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("provider config %s: %w", id, err)
		}
		if !cfg.IsZero() {
			cleaned[id] = cfg
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	return cleaned, nil
}

func validProviderConfigIDs(routes []Route) map[string]bool {
	known := make(map[string]bool, len(routes)*2)
	for _, r := range routes {
		known[r.Name] = true
		if r.Provider != "" {
			known[r.Provider] = true
		}
	}
	return known
}

func attachProviderConfigs(groups []providerGroup, configs map[string]proxyprofile.ClientProviderConfig) []providerGroup {
	if len(configs) == 0 {
		return groups
	}
	for i := range groups {
		cfg, ok := configs[groups[i].ID]
		if !ok || cfg.IsZero() {
			continue
		}
		copyCfg := cfg
		groups[i].Config = &copyCfg
		if rendered, err := proxyprofile.MarshalClientProviderYAML(cfg); err == nil {
			groups[i].ConfigYAML = rendered
		}
	}
	return groups
}

func cloneProviderConfigs(in map[string]proxyprofile.ClientProviderConfig) map[string]proxyprofile.ClientProviderConfig {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]proxyprofile.ClientProviderConfig, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeProviderConfigUpdates(existing map[string]proxyprofile.ClientProviderConfig, updates map[string]string, routes []Route) (map[string]proxyprofile.ClientProviderConfig, error) {
	known := validProviderConfigIDs(routes)
	merged := make(map[string]proxyprofile.ClientProviderConfig, len(existing)+len(updates))
	for id, cfg := range existing {
		if known[id] && !cfg.IsZero() {
			merged[id] = cfg
		}
	}
	for id, rawYAML := range updates {
		if !slug.MatchString(id) {
			return nil, fmt.Errorf("provider config key %q must be a slug of at most 64 characters", id)
		}
		if strings.TrimSpace(rawYAML) == "" {
			delete(merged, id)
			continue
		}
		cfg, err := proxyprofile.ParseClientProviderYAML([]byte(rawYAML))
		if err != nil {
			return nil, fmt.Errorf("provider config %s: %w", id, err)
		}
		if cfg.IsZero() {
			delete(merged, id)
		} else {
			merged[id] = cfg
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

// clientProviders serves caller-token-authenticated discovery for granted
// providers and their optional term-llm client_config metadata.
func (s *Service) clientProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		http.Error(w, "proxy concurrency limit", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-s.slots }()
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var callerID string
	if s.closing || s.store.db.QueryRow("SELECT id FROM callers WHERE hash=? AND revoked=0", hashToken(strings.TrimPrefix(values[0], "Bearer "))).Scan(&callerID) != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	accounts, err := s.store.accountInfos()
	if err != nil {
		http.Error(w, "provider discovery unavailable", http.StatusServiceUnavailable)
		return
	}
	activeByName := make(map[string]activeRoute, len(s.snap.routes))
	for _, ar := range s.snap.routes {
		activeByName[ar.Name] = ar
	}
	groups := providerGroups(s.snap.config.Routes, accounts)
	providers := make([]clientProviderInfo, 0, len(groups))
	for _, group := range groups {
		var granted []clientProviderRoute
		var primary *activeRoute
		for _, routeName := range group.Routes {
			ar, ok := activeByName[routeName]
			if !ok || !ar.Enabled {
				continue
			}
			var authority string
			if s.store.db.QueryRow("SELECT authority FROM grants WHERE caller=? AND route=?", callerID, ar.Name).Scan(&authority) != nil || authority != ar.authority {
				continue
			}
			granted = append(granted, clientProviderRoute{
				Name:        ar.Name,
				LocalPrefix: ar.LocalPrefix,
				WebSocket:   ar.WebSocket,
			})
			if primary == nil || ar.Name == group.ID {
				cp := ar
				primary = &cp
			}
		}
		if len(granted) == 0 || primary == nil {
			continue
		}
		entry := clientProviderInfo{
			ID:          group.ID,
			LocalPrefix: primary.LocalPrefix,
			WebSocket:   primary.WebSocket,
			Routes:      granted,
		}
		if cfg, ok := s.snap.config.Providers[group.ID]; ok && !cfg.IsZero() {
			cp := cfg
			entry.Config = &cp
		}
		providers = append(providers, entry)
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"revision":  s.snap.revision,
		"providers": providers,
	})
}

func (s *Service) updateProviderConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision int64  `json:"revision"`
		Provider string `json:"provider"`
		YAML     string `json:"yaml"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Provider = strings.TrimSpace(in.Provider)
	if !slug.MatchString(in.Provider) {
		fail(w, http.StatusBadRequest, "Provider ID must be a valid slug")
		return
	}
	var parsed proxyprofile.ClientProviderConfig
	if strings.TrimSpace(in.YAML) != "" {
		var err error
		parsed, err = proxyprofile.ParseClientProviderYAML([]byte(in.YAML))
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.ConfigFile != "" {
		fail(w, http.StatusConflict, "Provider config is operator-managed; edit the mounted file and Reload")
		return
	}
	if in.Revision != s.snap.revision {
		fail(w, http.StatusConflict, "Configuration changed in another tab; refresh before saving")
		return
	}
	nextCfg := s.snap.config
	nextCfg.Providers = cloneProviderConfigs(s.snap.config.Providers)
	if parsed.IsZero() {
		delete(nextCfg.Providers, in.Provider)
	} else {
		if nextCfg.Providers == nil {
			nextCfg.Providers = make(map[string]proxyprofile.ClientProviderConfig)
		}
		nextCfg.Providers[in.Provider] = parsed
	}
	next, err := s.snap.recompile(nextCfg, s.snap.secrets)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = s.store.saveConfigAndMoney(next); err != nil {
		fail(w, http.StatusServiceUnavailable, "Could not commit provider configuration; active config unchanged")
		return
	}
	s.snap = next
	var cfgPtr *proxyprofile.ClientProviderConfig
	var rendered string
	if !parsed.IsZero() {
		cp := parsed
		cfgPtr = &cp
		rendered, _ = proxyprofile.MarshalClientProviderYAML(parsed)
	}
	jsonReply(w, http.StatusOK, map[string]any{
		"revision":    next.revision,
		"config":      cfgPtr,
		"config_yaml": rendered,
	})
}

func (s *Service) parseProviderConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		YAML string `json:"yaml"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	parsed, err := proxyprofile.ImportTermLLMProviderYAML([]byte(in.YAML))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonReply(w, http.StatusOK, parsed)
}
