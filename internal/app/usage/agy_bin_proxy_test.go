package usage

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

func stubAgyUsageRunners(t *testing.T, cfg *config.Config) (direct, proxied *int) {
	t.Helper()
	oldLoad, oldLook, oldDirect, oldProxied := loadProviderUsageConfig, agyUsageLookPath, agyUsageCommand, agyUsageProxiedCommand
	t.Cleanup(func() {
		loadProviderUsageConfig, agyUsageLookPath, agyUsageCommand, agyUsageProxiedCommand = oldLoad, oldLook, oldDirect, oldProxied
	})
	direct, proxied = new(int), new(int)
	loadProviderUsageConfig = func() (*config.Config, error) { return cfg, nil }
	agyUsageLookPath = func(string) (string, error) { return "/bin/agy", nil }
	agyUsageCommand = func(context.Context, string) ([]byte, error) {
		*direct++
		return []byte(agyUsageFixture), nil
	}
	agyUsageProxiedCommand = func(_ context.Context, binary string, connection proxyrun.Connection) ([]byte, error) {
		*proxied++
		if binary != "/bin/agy" || connection.Token != usageProxyTestToken {
			t.Errorf("proxied usage binary=%q connection root=%q", binary, connection.Root)
		}
		return []byte(agyUsageFixture), nil
	}
	return direct, proxied
}

func TestAgyBinUsageUsesConfiguredProxyBinding(t *testing.T) {
	root := "https://proxy.example.test/agy-usage"
	if err := proxyclient.Register([]proxyclient.Binding{{Root: root, Kind: proxyclient.AgyNativeKind, Token: usageProxyTestToken}}); err != nil {
		t.Fatal(err)
	}
	direct, proxied := stubAgyUsageRunners(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"agy-bin": {ProxyBinding: root},
	}})
	var out bytes.Buffer
	if err := runAgyBinUsage(context.Background(), &out, false); err != nil {
		t.Fatalf("runAgyBinUsage: %v", err)
	}
	if *direct != 0 || *proxied != 1 {
		t.Fatalf("direct=%d proxied=%d; configured binding must use only the proxy", *direct, *proxied)
	}
	if !strings.Contains(out.String(), "Live through credential proxy") {
		t.Fatalf("output did not identify the proxy source:\n%s", out.String())
	}
}

func TestAgyBinUsageInvalidBindingNoFallback(t *testing.T) {
	direct, proxied := stubAgyUsageRunners(t, &config.Config{Providers: map[string]config.ProviderConfig{
		"agy-bin": {ProxyBinding: "https://unregistered.example.test/agy"},
	}})
	err := runAgyBinUsage(context.Background(), &bytes.Buffer{}, false)
	if err == nil || !strings.Contains(err.Error(), "no direct fallback") {
		t.Fatalf("error = %v", err)
	}
	if *direct != 0 || *proxied != 0 {
		t.Fatalf("direct=%d proxied=%d; invalid binding must not run agy", *direct, *proxied)
	}
}

func TestAgyBinUsageWithoutBindingStaysDirect(t *testing.T) {
	direct, proxied := stubAgyUsageRunners(t, &config.Config{})
	var out bytes.Buffer
	if err := runAgyBinUsage(context.Background(), &out, false); err != nil {
		t.Fatalf("runAgyBinUsage: %v", err)
	}
	if *direct != 1 || *proxied != 0 || !strings.Contains(out.String(), "Live from agy") {
		t.Fatalf("direct=%d proxied=%d output=%s", *direct, *proxied, out.String())
	}
}

func TestRedactAgyProxyToken(t *testing.T) {
	err := redactAgyProxyToken(errors.New("agy usage request failed: proxy http://r:"+usageProxyTestToken+"@h"), usageProxyTestToken)
	if strings.Contains(err.Error(), usageProxyTestToken) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("token not redacted: %v", err)
	}
}
