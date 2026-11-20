package proxy

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

type clientProviderRoute struct {
	Name        string `json:"name"`
	LocalPrefix string `json:"local_prefix"`
	Profile     string `json:"profile"`
	WebSocket   bool   `json:"websocket"`
}

type clientProviderInfo struct {
	ID     string                             `json:"id"`
	Family string                             `json:"family"`
	Routes []clientProviderRoute              `json:"routes"`
	Config *proxyprofile.ClientProviderConfig `json:"config,omitempty"`
}

type clientProviderDefault struct {
	Provider string `json:"provider"`
	Route    string `json:"route"`
}

func mergeProviderConfigUpdates(providers map[string]Provider, updates map[string]string) error {
	for id, raw := range updates {
		p, ok := providers[id]
		if !ok {
			return fmt.Errorf("unknown provider %s", id)
		}
		p.Client = nil
		if strings.TrimSpace(raw) != "" {
			cfg, err := proxyprofile.ParseClientProviderYAML([]byte(raw))
			if err != nil {
				return fmt.Errorf("provider config %s: %w", id, err)
			}
			if !cfg.IsZero() {
				p.Client = &cfg
			}
		}
		providers[id] = p
	}
	return nil
}

// clientProviders serves caller-token-authenticated discovery for granted
// providers and their optional term-llm client_config metadata.
func (s *Service) clientProviders(w http.ResponseWriter, caller Caller) {
	c := s.snap.config
	groups := providerRoutes(c)
	granted := make(map[string]string)
	byProvider := make(map[string][]clientProviderRoute)
	for _, ar := range s.snap.routes {
		if !ar.Enabled {
			continue
		}
		var authority string
		if s.store.db.QueryRow("SELECT authority FROM grants WHERE caller=? AND route=?", caller.ID, ar.Name).Scan(&authority) != nil || authority != ar.authority {
			continue
		}
		granted[ar.Name] = ar.Provider
		byProvider[ar.Provider] = append(byProvider[ar.Provider], clientProviderRoute{ar.Name, ar.LocalPrefix, ar.Profile, ar.WebSocket})
	}
	providers := make([]clientProviderInfo, 0, len(byProvider))
	for _, id := range slices.Sorted(maps.Keys(byProvider)) {
		p := c.Providers[id]
		providers = append(providers, clientProviderInfo{id, providerFamily(p, primaryProfile(id, groups[id])), byProvider[id], p.Client})
	}
	defaults := make(map[string]clientProviderDefault)
	for slot, name := range c.Defaults {
		if provider, ok := granted[name]; ok {
			defaults[slot] = clientProviderDefault{provider, name}
		}
	}
	jsonReply(w, http.StatusOK, map[string]any{"revision": s.snap.revision, "providers": providers, "defaults": defaults})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkConfigEdit(w, in.Revision) {
		return
	}
	nextCfg := s.snap.config
	nextCfg.Providers = maps.Clone(nextCfg.Providers)
	if err := mergeProviderConfigUpdates(nextCfg.Providers, map[string]string{in.Provider: in.YAML}); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
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
	cfg := nextCfg.Providers[in.Provider].Client
	var rendered string
	if cfg != nil {
		rendered, _ = proxyprofile.MarshalClientProviderYAML(*cfg)
	}
	jsonReply(w, http.StatusOK, map[string]any{"revision": next.revision, "config": cfg, "config_yaml": rendered})
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
