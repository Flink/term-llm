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
