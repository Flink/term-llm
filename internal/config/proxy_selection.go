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

// Only built-in providers own the global ancillary settings. In particular,
// selecting a named ChatGPT provider must not also redirect built-in ChatGPT
// (including its transcription and live settings).
func (c *Config) proxyFeatureSelected(profile proxyprofile.Profile, selected map[string]bool) bool {
	builtinSelected := func(name string) bool {
		builtin, ok := BuiltinProvider(name)
		return ok && selected[name] && InferProviderType(name, c.Providers[name].Type) == builtin.Type
	}
	if profile.Provider != "" {
		return builtinSelected(string(profile.Provider))
	}
	// A custom provider may use the same name as a standalone endpoint; its
	// selection is not permission to redirect the built-in feature too.
	_, configured := c.Providers[profile.ID]
	standalone := selected[profile.ID] && !configured
	switch profile.ID {
	case "chatgpt-backend", "chatgpt-openai":
		return standalone || builtinSelected("chatgpt")
	case "gemini-live":
		return standalone || builtinSelected("gemini")
	default:
		return standalone
	}
}

func (c *Config) proxyProviderSelection() (map[string]bool, error) {
	if c.Proxy.Providers == nil {
		return nil, nil
	}
	if len(c.Proxy.Direct) != 0 {
		return nil, fmt.Errorf("proxy.providers cannot be combined with proxy.direct; list the providers to proxy instead")
	}
	standalone := make(map[string]bool)
	for _, profile := range proxyprofile.All() {
		if profile.Provider == "" && !legacyOpenCodeProxyRoute(profile.ID) {
			standalone[profile.ID] = true
		}
	}
	selected := make(map[string]bool, len(c.Proxy.Providers))
	for _, name := range c.Proxy.Providers {
		pc, configured := c.Providers[name]
		if legacyOpenCodeProxyRoute(name) {
			return nil, fmt.Errorf("proxy.providers must select opencode-go as a whole, not %s", name)
		}
		if _, builtin := BuiltinProvider(name); configured || builtin {
			if _, ok := proxyprofile.ForProvider(InferProviderType(name, pc.Type)); !ok {
				return nil, fmt.Errorf("proxy.providers entry %q is not a supported proxy provider", name)
			}
		} else if !standalone[name] {
			if name == "claude-code" {
				return nil, fmt.Errorf(`proxy.providers entry "claude-code" is a route name; use "claude-bin"`)
			}
			return nil, fmt.Errorf("proxy.providers entry %q is not a known provider or integration", name)
		}
		if selected[name] {
			return nil, fmt.Errorf("proxy.providers entry %q is duplicated", name)
		}
		selected[name] = true
	}
	return selected, nil
}

func (c *Config) directProxyExceptions() (map[string]bool, error) {
	direct := make(map[string]bool, len(c.Proxy.Direct))
	if len(c.Proxy.Direct) == 0 {
		return direct, nil
	}
	known := map[string]bool{}
	for _, p := range proxyprofile.All() {
		known[p.ID] = true
		if p.Provider != "" {
			known[string(p.Provider)] = true
		}
	}
	for name := range c.Providers {
		known[name] = true
	}
	for _, name := range c.Proxy.Direct {
		if !known[name] || direct[name] {
			return nil, fmt.Errorf("proxy.direct must contain unique known integration names")
		}
		direct[name] = true
	}
	// Dynamic OpenCode Go uses one credential across its protocol subpaths.
	for _, id := range []string{"opencode-go-messages", "opencode-go-gemini"} {
		if direct[id] && !direct["opencode-go"] {
			return nil, fmt.Errorf("proxy.direct must select opencode-go as a whole, not only %s", id)
		}
	}
	return direct, nil
}
