package agyproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/mitmca"
)

func TestScopedUpstreamReceivesAllHostsAndRewrittenGeneration(t *testing.T) {
	t.Setenv(GenerationTraceFileEnv, "")
	ca, err := mitmca.New("offline native upstream")
	if err != nil {
		t.Fatal(err)
	}
	traceCA := filepath.Join(t.TempDir(), "upstream.pem")
	if err := os.WriteFile(traceCA, ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("personal:sk-tlp-fixture"))
		if r.Method != http.MethodConnect || r.Header.Get("Proxy-Authorization") != want {
			t.Error("request did not use authenticated scoped CONNECT")
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		switch r.Host {
		case "oauth2.googleapis.com:443", "www.googleapis.com:443", CloudCodeHost + ":443":
			serveScopedUpstreamFixture(t, ca, w, r)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer upstream.Close()
	endpoint, _ := url.Parse(upstream.URL)
	endpoint.User = url.UserPassword("personal", "sk-tlp-fixture")
	var server Server
	server.SetUpstream(endpoint.String(), traceCA)
	local, bundle, err := server.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	server.BeginTurn(true)
	// Reconfiguring a live instance cannot silently remove the scoped hop.
	server.SetUpstream("", "")
	localURL, _ := url.Parse(local)
	bundlePEM, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(bundlePEM)
	transport := &http.Transport{Proxy: http.ProxyURL(localURL), TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, target := range []string{
		"https://oauth2.googleapis.com/token",
		"https://www.googleapis.com/oauth2/v2/userinfo",
		"https://" + CloudCodeHost + "/v1internal:fetchAvailableModels",
		"https://" + CloudCodeHost + generationPath,
	} {
		resp, err := client.Post(target, "application/json", strings.NewReader(string(generationBody("run_command", "mcp_term-llm_shell"))))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status: %d", target, resp.StatusCode)
		}
	}
	if resp, err := client.Get("https://play.google.com/"); err == nil {
		resp.Body.Close()
		t.Fatal("a rejected background host unexpectedly connected")
	}
	if server.FilteredGenerations() != 1 {
		t.Fatalf("generation rewrite evidence = %d, want 1", server.FilteredGenerations())
	}
}

func serveScopedUpstreamFixture(t *testing.T, ca *mitmca.Authority, w http.ResponseWriter, connect *http.Request) {
	t.Helper()
	cert, err := ca.Leaf(strings.TrimSuffix(connect.Host, ":443"))
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	tunnel := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
	request, err := http.ReadRequest(bufio.NewReader(tunnel))
	if err != nil {
		t.Error(err)
		return
	}
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Error(err)
	}
	if request.URL.Path == generationPath && (strings.Contains(string(body), "run_command") || !strings.Contains(string(body), "mcp_term-llm_shell")) {
		t.Error("scoped native upstream received an unfiltered generation")
	}
	if request.Header.Get("Proxy-Authorization") != "" {
		t.Error("CONNECT credential leaked into the inner HTTPS request")
	}
	_, _ = fmt.Fprint(tunnel, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
}
