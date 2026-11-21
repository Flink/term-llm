package proxy

import (
	"fmt"

	"github.com/term-llm/term-llm/internal/providerspec"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

func providerRoutes(c Config) map[string][]Route {
	out := make(map[string][]Route, len(c.Providers))
	for _, r := range c.Routes {
		out[r.Provider] = append(out[r.Provider], r)
	}
	return out
}

func primaryProfile(id string, routes []Route) proxyprofile.Profile {
	var primary proxyprofile.Profile
	for _, r := range routes {
		p, ok := proxyprofile.ByID(r.Profile)
		if !ok {
			continue
		}
		if p.ID == id {
			return p
		}
		if primary.ID == "" || p.Parent == "" {
			primary = p
		}
	}
	return primary
}

func providerFamily(p Provider, primary proxyprofile.Profile) string {
	if primary.ID != "" {
		return primary.Family
	}
	if p.Family != "" {
		return p.Family
	}
	return "inference"
}

// validateConfigStructure is safe inside the schema migration: no upstream
// discovery, credential reads, or other I/O.
func validateConfigStructure(c Config) error {
	if len(c.Routes) > 128 || len(c.Secrets) > 256 {
		return fmt.Errorf("at most 128 routes and 256 secret references")
	}
	names := make(map[string]Route, len(c.Routes))
	for _, r := range c.Routes {
		if !slug.MatchString(r.Name) || names[r.Name].Name != "" {
			return fmt.Errorf("route names must be unique immutable slugs")
		}
		names[r.Name] = r
		if _, ok := c.Providers[r.Provider]; !ok {
			return fmt.Errorf("route %s must name an existing provider", r.Name)
		}
		if _, ok := proxyprofile.ByID(r.Profile); r.Profile != "" && !ok {
			return fmt.Errorf("route %s has an unknown profile", r.Name)
		}
		if err := validatePolicy(&r); err != nil {
			return fmt.Errorf("route %s: %w", r.Name, err)
		}
		for _, rule := range r.Rules {
			r.Allow = rule.Allow
			if err := validatePolicy(&r); err != nil {
				return fmt.Errorf("route %s rule: %w", r.Name, err)
			}
		}
	}
	groups := providerRoutes(c)
	for id, p := range c.Providers {
		if !slug.MatchString(id) || len(groups[id]) == 0 {
			return fmt.Errorf("provider %s must have a slug name and at least one route", id)
		}
		if r, ok := names[id]; ok && r.Provider != id {
			return fmt.Errorf("provider %s conflicts with a route owned by %s", id, r.Provider)
		}
		if err := validateProvider(id, p, groups[id]); err != nil {
			return fmt.Errorf("provider %s: %w", id, err)
		}
	}
	for slot, name := range c.Defaults {
		r := names[name]
		p, ok := proxyprofile.ByID(r.Profile)
		switch slot {
		case "search", "fetch", "image", "audio", "music", "transcription", "classify":
			if ok && r.Provider == proxyprofile.CanonicalParent(p.ID) && p.Slots[slot] != "" {
				continue
			}
		}
		return fmt.Errorf("invalid %s default: route %s must serve that slot for a canonical provider", slot, name)
	}
	return nil
}

func validateProvider(id string, p Provider, routes []Route) error {
	primary := primaryProfile(id, routes)
	family := providerFamily(p, primary)
	parent := proxyprofile.CanonicalParent(primary.ID)
	if primary.ID != "" && p.Family != "" {
		return fmt.Errorf("family must be omitted when a route has a profile")
	}
	if family == "classification" {
		return validateClassificationProvider(id, p, primary, routes)
	}
	inference := 0
	for _, r := range routes {
		profile, profiled := proxyprofile.ByID(r.Profile)
		if profiled && proxyprofile.CanonicalParent(profile.ID) != parent {
			return fmt.Errorf("route %s is outside the %s companion set", r.Name, parent)
		}
		if !profiled || proxyprofile.InferenceCapable(profile) {
			inference++
		}
	}
	builtin, isBuiltin := providerspec.Lookup(id)
	switch family {
	case "search", "media", "embeddings":
		if primary.ID == "" {
			profile, _ := proxyprofile.ByID(id)
			if profile.Family == family {
				parent = proxyprofile.CanonicalParent(profile.ID)
			}
		}
		if id != parent || isBuiltin || p.Client != nil {
			return fmt.Errorf("%s providers must use the canonical name and have no client hints", family)
		}
	case "inference":
		if isBuiltin && (primary.Provider != builtin.Type && (primary.ID != "" || p.Client == nil || p.Client.Type != builtin.Type)) {
			return fmt.Errorf("built-in name requires a primary route serving %s", builtin.Type)
		}
		if id != parent && inference != 1 {
			return fmt.Errorf("named inference providers must own exactly one inference-capable route")
		}
		if p.Client != nil {
			return p.Client.Validate()
		}
	default:
		return fmt.Errorf("unknown family")
	}
	return nil
}

// Classification discovery binds one canonical route per built-in selector.
// A profile-less provider is not a classifier, even if its ID now names a preset.
func validateClassificationProvider(id string, p Provider, primary proxyprofile.Profile, routes []Route) error {
	if len(routes) != 1 || primary.ID != id || primary.ClassifyProvider == "" || p.Client != nil {
		return fmt.Errorf("classification providers must use the canonical name, own one matching profiled route, and have no client hints")
	}
	if routes[0].WebSocket || len(routes[0].Rules) != 0 {
		return fmt.Errorf("classification routes cannot use WebSocket or subpath rules")
	}
	return nil
}

// Only prune references to routes that actually disappeared. New dangling
// entries remain validation errors rather than being silently discarded.
func pruneDeletedRoutes(c *Config, previous []Route) {
	remaining := providerRoutes(*c)
	names := make(map[string]bool, len(c.Routes))
	for _, route := range c.Routes {
		names[route.Name] = true
	}
	for _, old := range previous {
		if len(remaining[old.Provider]) == 0 {
			delete(c.Providers, old.Provider)
		}
		if !names[old.Name] {
			for slot, name := range c.Defaults {
				if name == old.Name {
					delete(c.Defaults, slot)
				}
			}
		}
	}
}
