package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxyrun"
)

// Exercise the installed binary with two clean device profiles, a single
// synthetic sign-in, the production launcher and an actual streamed response.
func TestClaudeSharedNativeBinary(t *testing.T) {
	binary := os.Getenv("TERM_LLM_TEST_CLAUDE_SHARED_BINARY")
	if binary == "" || runtime.GOOS != "linux" {
		t.Skip("set TERM_LLM_TEST_CLAUDE_SHARED_BINARY in a loopback-only Linux network namespace")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 || !filepath.IsAbs(binary) {
		t.Fatal("native compatibility test requires an absolute binary path and a loopback-only network namespace")
	}
	f := newClaudeSharedHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if p := claudeSharedOAuthRequest(t, r); p["grant_type"] != "authorization_code" || p["code"] != "binary-fixture-code" {
			t.Error("unexpected CLI authorization or refresh")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		claudeSharedIssueCode(w, 3600, sharedFixtureScope)
	}, claudeSharedBinaryAPI(t))
	s := f.service
	admin, csrf := loginAdmin(t, s)
	operator := claudeSharedSetup(t, s, admin, csrf, "create", "")
	discovery := f.discover(t, operator, http.StatusOK)
	claudeSharedCode(t, claudeSharedNativeClient(t, discovery, operator.Token), "binary-fixture-code", true)
	first := claudeSharedSetup(t, s, admin, csrf, "", discovery.Route)
	second := claudeSharedSetup(t, s, admin, csrf, "", discovery.Route)
	for _, device := range []nativeSetupReply{first, second} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = proxyrun.Run(ctx, proxyrun.Options{
			URL: f.server.URL + "/" + discovery.Route, Token: device.Token, Command: binary,
			Args: []string{"-p", "--safe-mode", "--setting-sources", "user", "--tools", "", "--no-session-persistence", "--model", "sonnet", "Synthetic fixture only"},
			Env:  []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"},
			In:   strings.NewReader(""), Out: &stdout, Err: &stderr,
		})
		cancel()
		if err != nil || strings.TrimSpace(stdout.String()) != "CLAUDE_SHARED_FIXTURE_OK" {
			t.Fatalf("isolated fresh device did not stream: %v; stdout=%q; stderr=%q", err, stdout.String(), stderr.String())
		}
	}
	if f.oauth.Load() != 1 {
		t.Fatal("client attempted a separate OAuth exchange")
	}
}

func claudeSharedBinaryAPI(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sharedFixtureAccess || r.Header.Get("X-API-Key") != "" {
			t.Error("fixture did not receive the shared OAuth credential exclusively")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/messages" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":[],"has_more":false}`)
			return
		}
		var prompt struct {
			Model  string
			Stream bool
		}
		if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil || !prompt.Stream {
			t.Error("CLI did not request a stream")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := [][2]string{
			{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"usage":{"input_tokens":1,"output_tokens":0},"content":[],"stop_reason":null}}`, prompt.Model)},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"CLAUDE_SHARED_FIXTURE_OK"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
		}
	}
}
