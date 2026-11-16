package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyFaviconAvailableBeforeSignIn(t *testing.T) {
	for _, auth := range []string{"token", "passkey"} {
		t.Run(auth, func(t *testing.T) {
			var s *Service
			origin := "http://localhost"
			if auth == "passkey" {
				s, _ = passkeyFixture(t)
				origin = passkeyOrigin
			} else {
				s = testService(t)
			}
			shell := httptest.NewRecorder()
			s.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, origin+"/_proxy/", nil))
			if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), `rel="icon" type="image/svg+xml" href="/_proxy/assets/favicon.svg"`) {
				t.Fatalf("missing favicon link: %d %s", shell.Code, shell.Body.String())
			}
			icon := httptest.NewRecorder()
			s.ServeHTTP(icon, httptest.NewRequest(http.MethodGet, origin+"/_proxy/assets/favicon.svg", nil))
			if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/svg+xml" || icon.Body.String() != proxyFavicon {
				t.Fatalf("favicon response: %d %v %s", icon.Code, icon.Header(), icon.Body.String())
			}
			for _, fragment := range []string{
				`width="64" height="64" rx="14" fill="#16634e"`,
				`d="m16 20 12 12-12 12"`,
				`cx="38.5" cy="44" r="4"`,
				`cx="51.5" cy="29" r="4"`,
				`d="M38.5 44 51.5 29"`,
			} {
				if !strings.Contains(icon.Body.String(), fragment) {
					t.Errorf("uplink favicon missing %q", fragment)
				}
			}
			if !strings.Contains(icon.Header().Get("Content-Security-Policy"), "img-src 'self';") {
				t.Fatal("favicon must work without relaxing image policy")
			}
		})
	}
}
