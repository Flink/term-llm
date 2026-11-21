package proxyclient

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgyNativeBootstrapRequiresExactVaultPolicy(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires a supported native worker platform")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ca := claudeNativeTestCA(t, func(cert *x509.Certificate) {
		cert.PermittedDNSDomains = agyNativePolicy.hosts
		cert.ExcludedDNSDomains = []string{".oauth2.googleapis.com", ".www.googleapis.com", ".daily-cloudcode-pa.googleapis.com"}
	})
	wrongCA := claudeNativeTestCA(t, nil)
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{name: "valid"},
		{"stale protocol", "protocol_version", 1},
		{"non-vault", "token_mode", "passthrough"},
		{"missing profile host", "hosts", []string{"oauth2.googleapis.com", "daily-cloudcode-pa.googleapis.com"}},
		{"extra host", "hosts", append(append([]string(nil), agyNativePolicy.hosts...), "play.google.com")},
		{"wrong CA", "certificate_pem", string(wrongCA)},
		{"wrong origin", "proxy_url", "https://unregistered.example.test"},
		{"wrong route", "route", "another/route"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/scoped/.well-known/term-llm/agy-proxy" || r.Header.Get("Authorization") != "Bearer "+claudeNativeTestToken {
					t.Error("bootstrap did not use the scoped agy metadata endpoint and bearer")
				}
				m := claudeNativeTestMetadata(ca, "http://"+r.Host, "personal")
				m["hosts"] = agyNativePolicy.hosts
				if tc.key != "" {
					m[tc.key] = tc.value
				}
				_ = json.NewEncoder(w).Encode(m)
			}))
			defer srv.Close()
			root := srv.URL + "/scoped"
			state, err := AgyNativeStateDir(root, claudeNativeTestToken)
			if err != nil {
				t.Fatal(err)
			}
			proxy, err := PrepareAgyNativeProxy(context.Background(), root, claudeNativeTestToken, state)
			if tc.key != "" {
				if err == nil || strings.Contains(err.Error(), claudeNativeTestToken) {
					t.Fatalf("unsafe bootstrap acceptance or error: %v", err)
				}
				if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
					t.Fatal("invalid metadata created state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(proxy.ProxyURL)
			if err != nil || u.User.String() != "personal:"+claudeNativeTestToken || filepath.Dir(proxy.CAFile) != state {
				t.Fatal("native proxy was not pinned to the scoped origin and CA")
			}
		})
	}
}

func TestAgyNativeRejectsRemoteHTTPBeforeBootstrap(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires a supported native worker platform")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := AgyNativeStateDir("http://remote.example.test/agy", claudeNativeTestToken); err == nil || !strings.Contains(err.Error(), "requires HTTPS") {
		t.Fatalf("remote plaintext proxy not rejected: %v", err)
	}
	for _, root := range []string{"https://remote.example.test/agy", "http://localhost:8080/agy", "http://[::1]:8080/agy", "http://127.0.0.1:8080/agy"} {
		if _, err := AgyNativeStateDir(root, claudeNativeTestToken); err != nil {
			t.Errorf("%s: %v", root, err)
		}
	}
}
