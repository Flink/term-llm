package proxy

import (
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func providerStatus(id string, routes []activeRoute, accounts map[string]AccountInfo, secrets map[string]material) (string, string) {
	var signIn, migration, refreshing, insecure, disabled, enabled bool
	now := time.Now().Unix()
	for _, r := range routes {
		if r.Provider != id {
			continue
		}
		disabled, enabled = disabled || !r.Enabled, enabled || r.Enabled
		if r.Account != "" {
			a := accounts[r.Account]
			if nativeProviderForKind(a.Kind) != nil && a.AuthMode != claudeSharedMode {
				migration = true
			} else {
				signIn = signIn || a.State != "ready" && a.State != "refreshing" || a.AuthMode == "token_only" && a.ExpiresAt > 0 && a.ExpiresAt <= now
				refreshing = refreshing || a.State == "refreshing"
			}
		}
		for _, dest := range r.destinations() {
			for _, rule := range dest.SetHeaders {
				if _, ok := secrets[rule.Secret]; rule.Secret != "" && !ok {
					return "red", "API key missing"
				}
			}
			insecure = insecure || r.Enabled && unencryptedUpstream(dest)
		}
	}
	switch {
	case signIn:
		return "red", "Sign-in needed"
	case migration:
		return "red", "Migration needed"
	case !enabled:
		return "grey", "Disabled"
	case refreshing:
		return "orange", "Refreshing sign-in"
	case insecure:
		return "orange", "Unencrypted upstream"
	case disabled:
		return "orange", "Partly disabled"
	default:
		return "green", ""
	}
}

func unencryptedUpstream(r activeRoute) bool {
	if r.target == nil || r.target.Scheme != "http" {
		return false
	}
	host := r.target.Hostname()
	ip, _ := netip.ParseAddr(host)
	return !strings.EqualFold(host, "localhost") && !ip.Unmap().IsLoopback()
}

type adminRouteInfo struct {
	Route
	Profile string   `json:"profile"`
	Serves  []string `json:"serves"`
}

func routeServes(r Route, family string) []string {
	p, _ := proxyprofile.ByID(r.Profile)
	keys := map[string]bool{}
	if family == "inference" && (p.ID == "" || proxyprofile.InferenceCapable(p)) {
		name := r.Provider
		if r.Provider == proxyprofile.CanonicalParent(p.ID) && p.Provider != "" {
			name = string(p.Provider)
		}
		keys["providers."+name+".base_url"] = true
		keys["providers."+name+".api_key"] = true
	}
	if r.Provider == proxyprofile.CanonicalParent(p.ID) {
		for _, b := range p.Bindings {
			if b.Root != "" {
				keys[b.Root] = true
			}
			if b.Key != "" {
				keys[b.Key] = true
			}
		}
		for slot, selector := range p.Slots {
			keys[slot+": "+selector] = true
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

type adminProviderInfo struct {
	ID         string                             `json:"id"`
	Label      string                             `json:"label"`
	Family     string                             `json:"family"`
	Status     string                             `json:"status"`
	Reason     string                             `json:"reason"`
	Routes     []string                           `json:"routes"`
	Config     *proxyprofile.ClientProviderConfig `json:"config,omitempty"`
	ConfigYAML string                             `json:"config_yaml,omitempty"`
}

func (s *snapshot) adminProviders(accounts []AccountInfo) ([]adminProviderInfo, []adminRouteInfo) {
	groups := providerRoutes(s.config)
	infos := make([]adminProviderInfo, 0, len(groups))
	byName := accountsByName(accounts)
	families := make(map[string]string, len(groups))
	for _, id := range slices.Sorted(maps.Keys(s.config.Providers)) {
		p := s.config.Providers[id]
		family := providerFamily(p, primaryProfile(id, groups[id]))
		families[id] = family
		state, reason := providerStatus(id, s.routes, byName, s.secrets)
		info := adminProviderInfo{ID: id, Label: p.Label, Family: family, Status: state, Reason: reason, Config: p.Client}
		for _, r := range groups[id] {
			info.Routes = append(info.Routes, r.Name)
		}
		if info.Config != nil {
			info.ConfigYAML, _ = proxyprofile.MarshalClientProviderYAML(*info.Config)
		}
		infos = append(infos, info)
	}
	routes := make([]adminRouteInfo, 0, len(s.config.Routes))
	for _, r := range s.config.Routes {
		routes = append(routes, adminRouteInfo{r, r.Profile, routeServes(r, families[r.Provider])})
	}
	return infos, routes
}
