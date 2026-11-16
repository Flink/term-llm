package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/serveui"
)

func TestProxyAssetVersioningAndCaching(t *testing.T) {
	s := testService(t)
	version := serveui.ProxyAssetVersion()

	shell := httptest.NewRecorder()
	s.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "http://localhost/_proxy/", nil))
	if shell.Code != http.StatusOK {
		t.Fatalf("shell status = %d", shell.Code)
	}
	if got := shell.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("shell Cache-Control = %q, want no-cache", got)
	}
	if !strings.Contains(shell.Body.String(), `href="/_proxy/assets/proxy.css?v=`+version+`"`) {
		t.Fatalf("shell missing versioned proxy.css link: %s", shell.Body.String())
	}
	if !strings.Contains(shell.Body.String(), `src="/_proxy/assets/proxy.js?v=`+version+`"`) {
		t.Fatalf("shell missing versioned proxy.js script: %s", shell.Body.String())
	}

	for _, name := range []string{"proxy.css", "proxy.js"} {
		versioned := httptest.NewRecorder()
		s.ServeHTTP(versioned, httptest.NewRequest(http.MethodGet, "http://localhost/_proxy/assets/"+name+"?v="+version, nil))
		if versioned.Code != http.StatusOK {
			t.Fatalf("%s versioned status = %d", name, versioned.Code)
		}
		if got := versioned.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("%s versioned Cache-Control = %q, want immutable", name, got)
		}

		unversioned := httptest.NewRecorder()
		s.ServeHTTP(unversioned, httptest.NewRequest(http.MethodGet, "http://localhost/_proxy/assets/"+name, nil))
		if unversioned.Code != http.StatusOK {
			t.Fatalf("%s unversioned status = %d", name, unversioned.Code)
		}
		if got := unversioned.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s unversioned Cache-Control = %q, want no-cache", name, got)
		}
	}
}
