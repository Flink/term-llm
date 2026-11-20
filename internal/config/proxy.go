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
	Tag          string   `mapstructure:"tag" yaml:"tag,omitempty"`
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
	if tag := c.Proxy.Tag; tag != "" {
		if err := proxyprofile.ValidateCallerTag(tag); err != nil {
			return err
		}
	}
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
	token, err = proxyprofile.TagCallerToken(token, c.Proxy.Tag)
	if err != nil {
		return err
	}
	catalog, err := discoverProxyProviders(root, token)
	if err != nil {
		return err
	}
	bound, err := c.bindDiscoveredProxy(root, token, catalog, file)
	if err != nil {
		return err
	}
	bindings = append(bindings, bound...)
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

func bindProxyProvider(name string, pc ProviderConfig, p proxyprofile.Profile, endpoint, token string, discovered *proxyprofile.ClientProviderConfig, file *fileSettings) (ProviderConfig, proxyclient.Binding, error) {
	if discovered != nil {
		var err error
		pc, err = mergeDiscoveredProviderConfig(name, pc, *discovered, file)
		if err != nil {
			return pc, proxyclient.Binding{}, err
		}
	}
	kind := InferProviderType(name, pc.Type)
	if p.Provider != "" {
		if pc.Type != "" && pc.Type != p.Provider && !pc.FromDefaults {
			return pc, proxyclient.Binding{}, fmt.Errorf("providers.%s.type (%s) conflicts with proxy route profile %s", name, pc.Type, p.ID)
		}
		kind = p.Provider
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
		return pc, proxyclient.Binding{}, nil
	}
	pc.Type = kind
	pc.BaseURL = endpoint
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
	return pc, proxyclient.Binding{Root: endpoint, Token: token, Kind: p.Account}, nil
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
