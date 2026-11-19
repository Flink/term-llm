package config

import (
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// ProxyConfig opts integrations into a trusted credential proxy. Providers is
// an allowlist when non-nil. Direct is the legacy proxy-all exception list.
type ProxyConfig struct {
	URL          string   `mapstructure:"url" yaml:"url,omitempty"`
	Token        string   `mapstructure:"token" yaml:"token,omitempty"`
	Direct       []string `mapstructure:"direct" yaml:"direct,omitempty"`
	Providers    []string `mapstructure:"providers" yaml:"providers,omitempty"`
	materialized bool
}

var (
	proxyRouteName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	proxySRVRecord = regexp.MustCompile(`(?i)^_[a-z0-9-]+\._tcp\.[a-z0-9-]+(\.[a-z0-9-]+)*\.?$`)
)

// ResolveProxySRV converts an explicit proxy discovery URL to HTTPS without
// changing its path. Other schemes are left to the caller's URL validation.
func ResolveProxySRV(raw string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "srv:") {
		return raw, nil
	}
	u, err := parseProxyRoot(raw)
	if err != nil {
		return "", err
	}
	if err := resolveProxySRVURL(u); err != nil {
		return "", err
	}
	return u.String(), nil
}

func proxyRoot(raw string) (string, error) {
	u, err := parseProxyRoot(raw)
	if err != nil {
		return "", err
	}
	return resolveProxyRoot(u)
}

func parseProxyRoot(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("proxy.url must be an HTTPS or srv:// root without credentials, query or fragment")
	}
	switch u.Scheme {
	case "srv":
		if !proxySRVRecord.MatchString(u.Host) || strings.Contains(raw, "#") {
			return nil, fmt.Errorf("proxy.url must use srv://_service._tcp.domain[/path] without credentials, port, query or fragment")
		}
	case "https":
	case "http":
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("proxy.url requires HTTPS except on loopback")
		}
	default:
		return nil, fmt.Errorf("proxy.url must be an HTTPS or srv:// root without credentials, query or fragment")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.Contains(part, "\\") {
			return nil, fmt.Errorf("proxy.url path must be canonical")
		}
	}
	return u, nil
}

func resolveProxyRoot(u *url.URL) (string, error) {
	resolved := *u
	if resolved.Scheme == "srv" {
		if err := resolveProxySRVURL(&resolved); err != nil {
			return "", err
		}
	}
	return strings.TrimRight(resolved.String(), "/"), nil
}

func resolveProxySRVURL(u *url.URL) error {
	host, err := lookupSRVAuthority(u.Host)
	if err != nil {
		return fmt.Errorf("proxy.url: %w", err)
	}
	u.Scheme, u.Host = "https", host
	return nil
}

func (c *Config) materializeProxy(file *fileSettings) error {
	if err := c.Proxy.readProviderSelection(file); err != nil {
		return err
	}
	bindings, err := c.materializeProviderProxies(file)
	if err != nil {
		return err
	}
	if c.Proxy.URL == "" {
		if c.Proxy.Token != "" || len(c.Proxy.Direct) > 0 || len(c.Proxy.Providers) > 0 {
			return fmt.Errorf("proxy.url is required when proxy settings are present")
		}
		return proxyclient.Register(bindings)
	}
	proxyURL, err := parseProxyRoot(c.Proxy.URL)
	if err != nil {
		return err
	}
	if err := c.validateProxyProviderSelectionSyntax(); err != nil {
		return err
	}
	if c.Proxy.Providers != nil && len(c.Proxy.Providers) == 0 {
		return proxyclient.Register(bindings)
	}
	root, err := resolveProxyRoot(proxyURL)
	if err != nil {
		return err
	}
	token, err := Cred(c.Proxy.Token).Resolve()
	if err != nil || len(token) < 16 || strings.ContainsAny(token, "\r\n\t ") {
		return fmt.Errorf("proxy.token must resolve to a private scoped client token (at least 16 characters)")
	}
	catalog, err := discoverProxyProviders(root, token)
	if err != nil {
		return err
	}
	selected, err := c.proxyProviderSelection(catalog)
	if err != nil {
		return err
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	direct, err := c.directProxyExceptions(catalog)
	if err != nil {
		return err
	}
	for _, spec := range BuiltinProviders() {
		if selected != nil && !selected[spec.Name] {
			continue
		}
		if _, ok := c.Providers[spec.Name]; !ok {
			c.Providers[spec.Name] = ProviderConfig{Type: spec.Type, FromDefaults: true}
		}
	}
	c.seedDiscoveredProxyProviders(catalog, selected, direct)
	for name, pc := range c.Providers {
		if selected != nil && !selected[name] {
			continue
		}
		if direct[name] || pc.Enabled != nil && !*pc.Enabled {
			continue
		}
		var providerBindings []proxyclient.Binding
		pc, providerBindings, err = bindProxyProvider(name, pc, root, token, catalog, direct, file)
		if err != nil {
			return err
		}
		c.Providers[name] = pc
		bindings = append(bindings, providerBindings...)
	}
	if err := validateProviderModels(c); err != nil {
		return err
	}
	for _, p := range proxyprofile.All() {
		// Legacy sibling profiles remain valid for old server configurations and
		// direct exceptions, but new clients use only the consolidated parent.
		if legacyOpenCodeProxyRoute(p.ID) || selected != nil && !c.proxyFeatureSelected(p, selected) {
			continue
		}
		if direct[p.ID] || p.Gate != "" || p.Provider != "" && direct[string(p.Provider)] {
			continue
		}
		// Standalone ancillary routes also receive an explicit binding;
		// no missing root may fall back to the public vendor endpoint.
		bindings = append(bindings, proxyclient.Binding{Root: root + "/" + p.ID, Token: token, Kind: p.Account})
		for _, b := range p.Bindings {
			if strings.HasPrefix(b.Root, "providers.") {
				continue
			} // named transcription providers use the provider loop above
			field, ok := proxyStringField(reflect.ValueOf(c).Elem(), strings.Split(b.Root, "."))
			if !ok {
				return fmt.Errorf("internal proxy binding has unknown config path %s", b.Root)
			}
			endpoint := root + "/" + p.ID + p.PathSuffix
			field.SetString(endpoint)
			if b.Key != "" {
				key, ok := proxyStringField(reflect.ValueOf(c).Elem(), strings.Split(b.Key, "."))
				if !ok {
					return fmt.Errorf("internal proxy credential binding has unknown path %s", b.Key)
				}
				key.SetString(token)
			}
			bindings = append(bindings, proxyclient.Binding{Root: endpoint, Token: token, Kind: p.Account})
		}
	}
	// Publish only after checking both local and shared routes for conflicts.
	if err := proxyclient.Register(bindings); err != nil {
		return err
	}
	c.Proxy.materialized = len(bindings) != 0
	// If nothing was bound and this config can still be saved, keep its
	// discovery reference instead of replacing it with a transient DNS result.
	if c.Proxy.materialized || proxyURL.Scheme != "srv" {
		c.Proxy.URL = root
	}
	return nil
}

func (c *Config) seedDiscoveredProxyProviders(catalog *proxyDiscoveryCatalog, selected, direct map[string]bool) {
	if catalog == nil {
		return
	}
	for id, discovered := range catalog.Providers {
		if selected != nil {
			if !selected[id] {
				continue
			}
		} else if direct[id] {
			continue
		}
		if isReservedProxyRouteID(id) && discovered.Config == nil {
			continue
		}
		if _, ok := c.Providers[id]; !ok {
			c.Providers[id] = ProviderConfig{FromDefaults: true}
		}
	}
}

func bindProxyProvider(name string, pc ProviderConfig, root, token string, catalog *proxyDiscoveryCatalog, direct map[string]bool, file *fileSettings) (ProviderConfig, []proxyclient.Binding, error) {
	orig := pc
	discovered, hasDiscovered := lookupDiscoveredProvider(catalog, name, "")
	if !hasDiscovered {
		if preProfile, ok := proxyprofile.ForProvider(InferProviderType(name, pc.Type)); ok && preProfile.ID != name {
			discovered, hasDiscovered = lookupDiscoveredProvider(catalog, name, preProfile.ID)
		}
	}
	if hasDiscovered && discovered.Config != nil {
		var err error
		pc, err = mergeDiscoveredProviderConfig(name, pc, *discovered.Config, file)
		if err != nil {
			return orig, nil, err
		}
	}
	kind := InferProviderType(name, pc.Type)
	p, ok := proxyprofile.ForProvider(kind)
	if !ok || direct[p.ID] || direct[string(kind)] {
		return orig, nil, nil
	}
	gate := p.Gate
	if (kind == ProviderTypeOpenAICompat || kind == ProviderTypeVLLM) && strings.TrimSpace(pc.URL) != "" {
		// A complete chat URL is not an API root. Replacing it with a base
		// would silently append /chat/completions and change its operation.
		gate = "exact url endpoints need a compatible base_url and an explicit trusted upstream route; proxy mode cannot infer an exact endpoint mapping"
	}
	if gate != "" {
		pc.Type = kind
		pc.FromDefaults = false
		pc.ProxyBinding = "gated:" + gate
		pc.APIKey, pc.ResolvedAPIKey = "", ""
		pc.Env = nil
		return pc, nil, nil
	}
	id := p.ID
	if _, builtInName := BuiltinProvider(name); !builtInName || kind == ProviderTypeOpenAICompat || kind == ProviderTypeVLLM {
		id = name
	}
	if !proxyRouteName.MatchString(id) {
		return orig, nil, fmt.Errorf("proxy provider name must be a route slug")
	}
	pc.Type = kind
	if hasDiscovered && discovered.LocalPrefix != "" {
		pc.BaseURL = root + strings.TrimRight(discovered.LocalPrefix, "/")
	} else {
		pc.BaseURL = root + "/" + id
	}
	pc.URL = ""
	pc.ResolvedURL = ""
	pc.ProxyBinding = pc.BaseURL
	pc.FromDefaults = false
	pc.APIKey = token
	pc.ResolvedAPIKey = token
	pc.Headers = nil
	pc.Env = nil
	if p.CLIRootEnv != "" {
		pc.Env = map[string]string{p.CLIRootEnv: pc.BaseURL}
	}
	pc.credentialsResolved = true
	pc.needsLazyResolution = false
	pc.AccessKey = ""
	pc.SecretKey = ""
	pc.SessionToken = ""
	pc.Profile = ""
	if p.Account != "" {
		pc.APIKey = ""
		pc.ResolvedAPIKey = ""
	}
	if kind == ProviderTypeAnthropic {
		pc.Credentials = "api_key"
	}
	bindings := []proxyclient.Binding{{Root: pc.BaseURL, Token: token, Kind: p.Account}}
	if kind == ProviderTypeOpenCodeGo {
		bindings = append(bindings, proxyclient.Binding{Root: pc.BaseURL + "/messages", Token: token}, proxyclient.Binding{Root: pc.BaseURL + "/gemini", Token: token})
	}
	return pc, bindings, nil
}

func proxyStringField(value reflect.Value, path []string) (reflect.Value, bool) {
	if value.Kind() != reflect.Struct || len(path) == 0 {
		return reflect.Value{}, false
	}
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("mapstructure"), ",")[0]
		if name != path[0] {
			continue
		}
		field := value.Field(i)
		if len(path) == 1 {
			return field, field.Kind() == reflect.String && field.CanSet()
		}
		return proxyStringField(field, path[1:])
	}
	return reflect.Value{}, false
}

func (p *ProviderConfig) resolveProxyCredentials(name string) error {
	if strings.HasPrefix(p.ProxyBinding, "gated:") {
		return fmt.Errorf("provider %q cannot run credentiallessly through the proxy yet: %s; explicitly list it in proxy.direct only if local credential custody is intended", name, strings.TrimPrefix(p.ProxyBinding, "gated:"))
	}
	p.credentialsResolved = true
	p.needsLazyResolution = false
	return nil
}

// inferenceReadyError guards runtime delegation before the ordinary lazy-value
// fast path. Factory callers must not bypass a fail-closed transport gate.
func (p *ProviderConfig) inferenceReadyError() error {
	if p.ProxyBinding != "" {
		return p.resolveProxyCredentials(string(p.Type))
	}
	return nil
}
