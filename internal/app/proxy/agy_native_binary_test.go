package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/providers/clibin"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

// This opt-in installed-binary check refuses external egress and never uses a
// real Google login. Set an explicit absolute TERM_LLM_TEST_AGY_BINARY path.
// Linux requires a loopback-only network namespace and exercises native login;
// macOS exercises only shared workers through the production sandbox launcher.
func TestAntigravityNativeBinary(t *testing.T) {
	binary := os.Getenv("TERM_LLM_TEST_AGY_BINARY")
	if binary == "" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("set TERM_LLM_TEST_AGY_BINARY on an isolated Linux client or macOS")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native compatibility test requires an absolute binary path")
	}
	if runtime.GOOS == "linux" {
		interfaces, err := net.Interfaces()
		if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
			t.Fatal("native compatibility test requires a loopback-only network namespace")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	binDir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(binDir, "agy")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
	proxy, root, operator := agyTestProxy(t, http.HandlerFunc(agyNativeBinaryUpstream))
	launchRoot := root + "/agy"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if runtime.GOOS == "linux" {
		var stdout, stderr bytes.Buffer
		err := proxyrun.RunAgy(ctx, proxyrun.Options{URL: launchRoot, Token: operator, Command: binary, Args: []string{"acp", "--login", "--disable-telemetry"}, In: strings.NewReader("fixture-code\n"), Out: &stdout, Err: &stderr})
		if err != nil || !strings.Contains(stdout.String(), "Logged in as fixture@example.invalid") {
			t.Fatalf("isolated native login: %v, stdout %s, stderr %s", err, stdout.String(), stderr.String())
		}
	} else {
		// Standalone proxy run (including operator --login) remains Linux-only.
		// Seed custody through the fixture's CONNECT client, never an agy child.
		bootstrap := agyTestDiscover(t, root, operator, http.StatusOK)
		client := agyTestHTTPClient(t, root, operator, bootstrap)
		status, body := agyTestOAuth(t, client, url.Values{
			"grant_type": {"authorization_code"}, "code": {"fixture-code"},
			"code_verifier": {"fixture-verifier"}, "redirect_uri": {"https://antigravity.google/oauth-callback"},
		})
		var login claudeVaultReply
		if status != http.StatusOK || json.Unmarshal([]byte(body), &login) != nil ||
			!strings.HasPrefix(login.AccessToken, agyProxyProvider.accessPrefix) ||
			strings.Contains(body, agyTestAccess) || strings.Contains(body, agyTestRefresh) {
			t.Fatalf("synthetic shared enrollment: %d %s", status, body)
		}
	}
	// Use a new local profile and new grant. The worker never runs --login.
	workerHome := t.TempDir()
	t.Setenv("HOME", workerHome)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(workerHome, "cache"))
	token := caller(t, proxy, "agy")
	if err := proxyclient.Register([]proxyclient.Binding{{Root: launchRoot, Token: token, Kind: "agy-bin"}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		// Fail before any child (including --version) when production support is
		// unavailable. All agy execution below must use the bound provider's
		// sandbox-exec path, not a test-only wrapper or the standalone launcher.
		if _, err := proxyclient.AgyNativeStateDir(launchRoot, token); err != nil {
			t.Fatalf("macOS shared worker sandbox support is required before executing agy: %v", err)
		}
	}
	provider := clibin.NewAgyBinProvider("gemini-2.5-pro", map[string]string{"TERM_LLM_AGY_PROXY_ROOT": launchRoot})
	models, err := provider.ListModels(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "gemini-2.5-pro" {
		t.Fatalf("proxied model listing: %+v, %v", models, err)
	}
	stream, err := provider.Stream(ctx, core.Request{Messages: []core.Message{core.UserText("Return the synthetic fixture marker.")}, Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var reply strings.Builder
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == core.EventTextDelta {
			reply.WriteString(event.Text)
		}
	}
	if strings.TrimSpace(reply.String()) != "AGY_FIXTURE_OK" {
		t.Fatalf("unexpected native response: %q", reply.String())
	}
}

func agyNativeBinaryUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Host == agyOAuthHost && r.URL.Path == "/token" && r.ParseForm() == nil {
		switch {
		case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "fixture-code" && r.Form.Get("code_verifier") != "":
			io.WriteString(w, `{"access_token":"fixture-google-access","refresh_token":"fixture-google-refresh","expires_in":1,"token_type":"Bearer"}`)
		case r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == agyTestRefresh:
			io.WriteString(w, `{"access_token":"fixture-google-renewed","expires_in":3600,"token_type":"Bearer"}`)
		default:
			http.Error(w, "fixture rejected token request", 400)
		}
		return
	}
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+agyTestAccess && auth != "Bearer fixture-google-renewed" {
		http.Error(w, "fixture auth required", 401)
		return
	}
	switch r.Host + r.URL.Path {
	case agyProfileHost + agyProfilePath:
		io.WriteString(w, `{"id":"12345","email":"fixture@example.invalid","name":"Fixture User","verified_email":true}`)
	case agyAPIHost + "/v1internal:loadCodeAssist":
		io.WriteString(w, `{"cloudaicompanionProject":"fixture-project","currentTier":{"id":"free-tier"}}`)
	case agyAPIHost + "/v1internal:fetchAvailableModels":
		io.WriteString(w, `{"models":{"gemini-2.5-pro":{"displayName":"Synthetic Pro","model":"MODEL_GOOGLE_GEMINI_2_5_PRO","apiProvider":"API_PROVIDER_GOOGLE_GEMINI"}},"defaultAgentModelId":"gemini-2.5-pro","agentModelSorts":[{"groups":[{"modelIds":["gemini-2.5-pro"]}]}]}`)
	case agyAPIHost + "/v1internal:fetchUserInfo":
		io.WriteString(w, `{}`)
	case agyAPIHost + "/v1internal:streamGenerateContent":
		var payload struct {
			Model   string
			Project string
			Request json.RawMessage
		}
		if r.URL.RawQuery != "alt=sse" || auth != "Bearer fixture-google-renewed" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "gemini-2.5-pro" || payload.Project != "fixture-project" || !bytes.Contains(payload.Request, []byte("synthetic fixture marker")) {
			http.Error(w, "unexpected fixture generation request", 422)
			return
		}
		var filtered struct{ Tools []json.RawMessage }
		if json.Unmarshal(payload.Request, &filtered) != nil || len(filtered.Tools) != 0 {
			http.Error(w, "worker forwarded tools in a tool-free turn", 422)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"AGY_FIXTURE_OK\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":4,\"totalTokenCount\":14}}}\n\n")
	default:
		http.NotFound(w, r)
	}
}
