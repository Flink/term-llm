package config

import (
	"strings"
	"testing"
)

func TestProxyClassificationSelectionDoesNotBindSameNamedInferenceAlias(t *testing.T) {
	local := "providers:\n  typesafe:\n    type: openai\n    base_url: https://chat.example.test/v1\n"
	for _, providers := range [][]proxyDiscoveredProvider{
		nil,
		{familyProvider("typesafe", "classification", familyRoute("typesafe", "typesafe", "/jev/"))},
	} {
		cfg, _ := loadFamilyProxy(t, proxyDiscoveryPayload{Providers: providers}, "  providers: [typesafe]\n", local)
		if p := cfg.Providers["typesafe"]; p.BaseURL != "https://chat.example.test/v1" || p.ProxyBinding != "" {
			t.Fatal("classifier selection also changed inference alias")
		}
	}
}

func TestProxyDiscoveryRejectsMisclassifiedKnownRoutes(t *testing.T) {
	for _, p := range []proxyDiscoveredProvider{
		familyProvider("typesafe", "inference", familyRoute("typesafe", "typesafe", "/jev/")),
		familyProvider("jina", "classification", familyRoute("jina", "jina", "/embed/")),
		familyProvider("wrong-owner", "classification", familyRoute("typesafe", "typesafe", "/jev/")),
	} {
		if _, err := validateDiscoveryPayload(proxyDiscoveryPayload{Providers: []proxyDiscoveredProvider{p}}); err == nil || !strings.Contains(err.Error(), "invalid classification route") {
			t.Errorf("malformed provider %q (%s) was not rejected: %v", p.ID, p.Family, err)
		}
	}
}

func TestProxyClassifierDefaultRequiresExplicitSettingAndBoundRoute(t *testing.T) {
	providers := []proxyDiscoveredProvider{
		familyProvider("openai-decisions", "classification", familyRoute("openai-decisions", "openai-decisions", "/decisions/")),
	}
	for _, tc := range []struct {
		name, proxyExtra, local, want string
		setDefault, bound             bool
	}{
		{name: "discovery alone", want: "typesafe", bound: true},
		{name: "configured on proxy", want: "openai", setDefault: true, bound: true},
		{name: "local override", local: "classify:\n  default_provider: typesafe\n", want: "typesafe", setDefault: true, bound: true},
		{name: "direct exception", proxyExtra: "  direct: [openai-decisions]\n", want: "typesafe", setDefault: true},
		{name: "allowlist excludes route", proxyExtra: "  providers: [typesafe]\n", want: "typesafe", setDefault: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := proxyDiscoveryPayload{Providers: providers}
			if tc.setDefault {
				payload.Defaults = map[string]proxyDiscoveredDefault{"classify": {Provider: "openai-decisions", Route: "openai-decisions"}}
			}
			cfg, root := loadFamilyProxy(t, payload, tc.proxyExtra, tc.local)
			p, err := cfg.Classify.ResolveProvider("")
			if err != nil || p.Type != tc.want {
				t.Fatalf("default classifier = %q, %v; want %q", p.Type, err, tc.want)
			}
			p, err = cfg.Classify.ResolveProvider("openai")
			if err != nil {
				t.Fatal(err)
			}
			endpoint, token, managed, err := p.ProxyConnection()
			if err != nil || managed != tc.bound || tc.bound && (endpoint != root+"/decisions" || token != familyProxyToken) {
				t.Errorf("Decisions binding: endpoint %q, managed = %v, err = %v", endpoint, managed, err)
			}
		})
	}
}
