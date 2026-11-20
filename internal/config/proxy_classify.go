package config

import (
	"fmt"

	"github.com/term-llm/term-llm/internal/proxyclient"
	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// classifyProxyBinding lives alongside source configuration, so resolving a
// managed classifier never overwrites or serializes a local vendor credential.
type classifyProxyBinding struct {
	profile  string
	endpoint string
	token    string
}

// ProxyConnection returns a managed classifier's approved endpoint and token.
// An explicitly selected but ungranted profile errors before any local vendor
// credentials or endpoints can be resolved.
func (p ClassifyProviderConfig) ProxyConnection() (endpoint, token string, managed bool, err error) {
	b := p.proxyBinding
	if b.profile == "" {
		return "", "", false, nil
	}
	if b.endpoint == "" {
		return "", "", true, fmt.Errorf("proxy classification provider %q is not enabled and granted to this client", b.profile)
	}
	return b.endpoint, b.token, true, nil
}

// CredentialsConfigured checks for a managed grant or direct credential
// references without resolving a secret.
func (p ClassifyProviderConfig) CredentialsConfigured() bool {
	if b := p.proxyBinding; b.profile != "" {
		return b.endpoint != ""
	}
	return p.Key().Configured() && (p.Type != ClassifyProviderCloudflare || p.AccountIDRef().Configured())
}

func (c *ClassifyConfig) bindProxy(root, token string, requested []string, catalog *proxyDiscoveryCatalog) ([]proxyclient.Binding, error) {
	// An omitted list means proxy-all for other integrations. Classifiers
	// require their exact profile ID in the list and do not use route aliases.
	c.proxyBindings = nil
	var bindings []proxyclient.Binding
	for _, id := range requested {
		p, known := proxyprofile.ByID(id)
		if !known || p.ClassifyProvider == "" || p.Gate != "" {
			continue
		}
		b := classifyProxyBinding{profile: p.ID}
		for _, r := range catalog.Providers[p.ID].Routes {
			if r.Profile != p.ID {
				continue
			}
			if b.endpoint != "" {
				return nil, fmt.Errorf("proxy discovery advertises multiple classification routes for %q", p.ID)
			}
			b.endpoint = (discoveredProxyRoute{route: r}).endpoint(root)
			b.token = token
			bindings = append(bindings, proxyclient.Binding{Root: b.endpoint, Token: token, Kind: "classification:" + p.ID})
		}
		if c.proxyBindings == nil {
			c.proxyBindings = make(map[string]classifyProxyBinding)
		}
		c.proxyBindings[p.ClassifyProvider] = b
	}
	return bindings, nil
}
