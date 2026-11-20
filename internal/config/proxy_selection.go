package config

import (
	"fmt"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// The file's raw value distinguishes an omitted list (legacy proxy-all) from
// an empty one (proxy nothing). Reject null and weakly typed lists rather than
// silently proxying more integrations than requested.
func (p *ProxyConfig) readProviderSelection(file *fileSettings) error {
	if file == nil {
		return nil
	}
	section, _ := file.lookup("proxy")
	mapping, _ := section.(map[string]any)
	raw, present := mapping["providers"]
	if !present {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("proxy.providers must be a list of provider names (use [] to proxy none)")
	}
	p.Providers = make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return fmt.Errorf("proxy.providers must contain only provider names")
		}
		p.Providers = append(p.Providers, name)
	}
	return nil
}

func legacyOpenCodeProxyRoute(id string) bool {
	return id == "opencode-go-messages" || id == "opencode-go-gemini"
}

func (c *Config) validateProxyProviderSelectionSyntax() error {
	if c.Proxy.Providers == nil {
		directSet := make(map[string]bool, len(c.Proxy.Direct))
		for _, name := range c.Proxy.Direct {
			directSet[name] = true
		}
		for _, id := range []string{"opencode-go-messages", "opencode-go-gemini"} {
			if directSet[id] && !directSet["opencode-go"] {
				return fmt.Errorf("proxy.direct must select opencode-go as a whole, not only %s", id)
			}
		}
		return nil
	}
	if len(c.Proxy.Direct) != 0 {
		return fmt.Errorf("proxy.providers cannot be combined with proxy.direct; list the providers to proxy instead")
	}
	seen := make(map[string]bool, len(c.Proxy.Providers))
	for _, name := range c.Proxy.Providers {
		if legacyOpenCodeProxyRoute(name) {
			return fmt.Errorf("proxy.providers must select opencode-go as a whole, not %s", name)
		}
		if seen[name] {
			return fmt.Errorf("proxy.providers entry %q is duplicated", name)
		}
		seen[name] = true
	}
	return nil
}

// A stored provider selection and a profile selection are different: a named
// inference duplicate can bind its own provider entry but not global keys.
type proxySelection struct {
	all       bool
	providers map[string]bool
	aliases   map[string]bool
	profiles  map[string]bool
}

func expandProxyProfile(names map[string]bool, id string) {
	names[id] = true
	for _, p := range proxyprofile.Companions(id) {
		names[p.ID] = true
	}
}

func canonicalProxyProfile(provider, profile string) bool {
	return provider == proxyprofile.CanonicalParent(profile) || provider == profile
}

func (s proxySelection) includes(p proxyDiscoveredProvider, r proxyDiscoveredRoute) bool {
	return s.all || s.providers[p.ID] || s.aliases[r.Name]
}

func (c *Config) proxyProviderSelection(catalog *proxyDiscoveryCatalog) (proxySelection, error) {
	selected := proxySelection{all: c.Proxy.Providers == nil, providers: map[string]bool{}, aliases: map[string]bool{}, profiles: map[string]bool{}}
	for _, name := range c.Proxy.Providers {
		if provider, ok := catalog.Providers[name]; ok {
			selected.providers[name] = true
			for _, r := range provider.Routes {
				if r.Profile != "" && canonicalProxyProfile(name, r.Profile) {
					expandProxyProfile(selected.profiles, name)
					break
				}
			}
			continue
		}
		if profile, ok := catalog.inferenceRouteAlias(name); ok {
			selected.aliases[name] = true
			selected.profiles[profile] = true
			continue
		}
		if _, ok := proxyprofile.ByID(name); ok {
			expandProxyProfile(selected.profiles, name)
			continue
		}
		if builtin, ok := BuiltinProvider(name); ok {
			if p, ok := proxyprofile.ForProvider(builtin.Type); ok {
				expandProxyProfile(selected.profiles, p.ID)
				continue
			}
		}
		return selected, fmt.Errorf("proxy.providers entry %q is not a known provider or integration", name)
	}
	return selected, nil
}

func (c *proxyDiscoveryCatalog) inferenceRouteAlias(name string) (string, bool) {
	builtin, ok := BuiltinProvider(name)
	if !ok {
		return "", false
	}
	for _, p := range c.Providers {
		for _, r := range p.Routes {
			profile, _ := proxyprofile.ByID(r.Profile)
			if r.Name == name && profile.Provider == builtin.Type && canonicalProxyProfile(p.ID, profile.ID) {
				return profile.ID, true
			}
		}
	}
	return "", false
}

func (c *Config) directProxyExceptions(catalog *proxyDiscoveryCatalog) (map[string]bool, error) {
	direct := make(map[string]bool, len(c.Proxy.Direct))
	seen := make(map[string]bool, len(c.Proxy.Direct))
	for _, name := range c.Proxy.Direct {
		_, profile := proxyprofile.ByID(name)
		builtin, builtinName := BuiltinProvider(name)
		_, configured := c.Providers[name]
		_, discovered := catalog.Providers[name]
		if seen[name] || !(profile || builtinName || configured || discovered) {
			return nil, fmt.Errorf("proxy.direct must contain unique known integration names")
		}
		seen[name] = true
		expandProxyProfile(direct, name)
		if p, ok := proxyprofile.ForProvider(builtin.Type); ok {
			expandProxyProfile(direct, p.ID)
		}
	}
	return direct, nil
}
