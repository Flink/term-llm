package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

// ProviderProxyConfig opts a native claude-bin provider into a scoped proxy
// route. Token retains its credential reference; the resolved secret lives only
// in the process-local proxyclient registry, never in the provider environment.
type ProviderProxyConfig struct {
	URL   string `mapstructure:"url" yaml:"url,omitempty"`
	Token string `mapstructure:"token" yaml:"token,omitempty"`
}

// providerProxyRoot validates a route root, not just the proxy server origin.
// Keep this stricter than the global root, which appends integration routes.
func providerProxyRoot(raw string) (string, error) {
	root, err := proxyRoot(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(root)
	if err != nil || u.Path == "" || strings.Contains(u.Path, "//") || strings.Contains(u.EscapedPath(), "%") {
		return "", fmt.Errorf("proxy.url must include a canonical route path")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("proxy.url has an invalid port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("proxy.url has an invalid port")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return u.String(), nil
}

func (c *Config) materializeProviderProxies(file *fileSettings) ([]proxyclient.Binding, error) {
	updates := make(map[string]ProviderConfig)
	var bindings []proxyclient.Binding
	for name, pc := range c.Providers {
		// Merging schema defaults drops empty maps. An explicit proxy: {}
		// is still an opt-in and must fail for missing URL/token.
		if pc.Proxy == nil && file != nil && file.has("providers."+name+".proxy") {
			pc.Proxy = &ProviderProxyConfig{}
		}
		if pc.Proxy == nil {
			continue
		}
		field := "providers." + name + ".proxy"
		if InferProviderType(name, pc.Type) != ProviderTypeClaudeBin {
			return nil, fmt.Errorf("%s is only supported for type claude-bin", field)
		}
		if c.Proxy.Providers == nil && (c.Proxy.URL != "" || c.Proxy.Token != "" || len(c.Proxy.Direct) != 0) {
			return nil, fmt.Errorf("%s cannot be combined with global proxy settings without an explicit proxy.providers list", field)
		}
		if slices.Contains(c.Proxy.Providers, name) {
			return nil, fmt.Errorf("%s is already configured; remove %q from proxy.providers to use its own route and token", field, name)
		}
		root, err := providerProxyRoot(pc.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		token, err := Cred(pc.Proxy.Token).Resolve()
		if err != nil || len(token) < 16 || strings.ContainsAny(token, "\r\n\t ") || strings.HasPrefix(token, "admin_") || proxyclient.ValidateClaudeNativeToken(token) != nil {
			// Credential resolver errors can contain command output or raw
			// references. Do not expose them or fall back to Claude credentials.
			return nil, fmt.Errorf("%s.token must resolve to a private scoped client token (at least 16 characters)", field)
		}
		pc.Type = ProviderTypeClaudeBin
		pc.BaseURL = root
		pc.URL, pc.ResolvedURL = "", ""
		pc.ProxyBinding = root
		pc.FromDefaults = false
		pc.APIKey, pc.ResolvedAPIKey = "", ""
		pc.Headers = nil
		env := make(map[string]string, len(pc.Env)+1)
		for key, value := range pc.Env {
			env[key] = value
		}
		env["TERM_LLM_CLAUDE_PROXY_ROOT"] = root
		pc.Env = env
		pc.credentialsResolved = true
		pc.needsLazyResolution = false
		updates[name] = pc
		bindings = append(bindings, proxyclient.Binding{Root: root, Token: token, Kind: "claude-code"})
	}
	for name, pc := range updates {
		c.Providers[name] = pc
	}
	return bindings, nil
}
