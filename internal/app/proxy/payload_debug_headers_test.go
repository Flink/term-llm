package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/app/proxy/debugheaders"
)

func TestPayloadDebugShowsWhetherOpenCodeGoKeyWasSent(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
	}{{"missing substitution", ""}, {"substituted", "key"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t)
			cookie, csrf := loginAdmin(t, s)
			const vendor = "vendor-key-never-archive"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if tc.secret != "" {
					want = "Bearer " + vendor
				}
				if r.Header.Get("Authorization") != want || r.Header.Get("X-Opencode-Session") != "session-never-archive" {
					t.Error("unexpected auth or attribution at upstream")
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="private-challenge"`)
				w.Header().Set("Set-Cookie", "private-cookie")
				http.Error(w, "fixture upstream error", http.StatusUnauthorized)
			}))
			defer up.Close()
			r := openCodeGoPreset(t).newRoute("opencode-go", "opencode-go", tc.secret)
			r.AllowHTTP = true
			for i := range r.Rules {
				r.Rules[i].Upstream = up.URL
			}
			configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, vendor)})
			enableDebugAdmin(t, s, cookie, csrf)
			token := caller(t, s, r.Name)
			req := httptest.NewRequest("POST", "/opencode-go/chat/completions?secret=private-query", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "fixture-agent/1.0")
			req.Header.Set("X-Opencode-Client", "term-llm")
			req.Header.Set("X-Opencode-Session", "session-never-archive")
			req.Header.Set("X-Private-Auth", "private-custom-credential")
			out := httptest.NewRecorder()
			s.ServeHTTP(out, req)
			id := out.Header().Get("X-Term-LLM-Proxy-Request-ID")
			if out.Code != http.StatusUnauthorized || id == "" {
				t.Fatalf("want forwarded 401 with request ID, got %d (%s)", out.Code, out.Body.String())
			}
			inspect := func(file string) debugheaders.Summary {
				t.Helper()
				out := adminRequest(s, "GET", "debug/captures/"+id+"/"+file, nil, cookie, csrf)
				var preview struct{ Content string }
				if err := json.Unmarshal(out.Body.Bytes(), &preview); err != nil || out.Code != 200 {
					t.Fatalf("cannot preview %s: %d %v", file, out.Code, err)
				}
				for _, secret := range []string{token, vendor, "session-never-archive", "private-query", "private-custom-credential", "private-cookie", "private-challenge"} {
					if strings.Contains(preview.Content, secret) {
						t.Fatalf("%s stored a sensitive value", file)
					}
				}
				var headers debugheaders.Summary
				if err := json.Unmarshal([]byte(preview.Content), &headers); err != nil {
					t.Fatalf("decode %s: %v", file, err)
				}
				return headers
			}
			before := inspect(debugheaders.Request)
			after := inspect(debugheaders.UpstreamRequest)
			response := inspect(debugheaders.UpstreamResponse)
			if before.AuthorizationScheme != "bearer" || debugTestHeader(before, "Authorization").Substituted || debugTestHeader(before, "Authorization").Value != "Bearer "+token[:3]+"…" || debugTestHeader(before, "X-Private-Auth").Value != "[redacted]" {
				t.Fatalf("wrong incoming metadata: %+v", before)
			}
			wantAuth := debugheaders.Field{}
			if tc.secret != "" {
				wantAuth = debugheaders.Field{Name: "Authorization", Count: 1, NonEmpty: true, Substituted: true, Value: "Bearer ven…"}
			}
			if got := debugTestHeader(after, "Authorization"); got != wantAuth || (after.AuthorizationScheme == "bearer") != (tc.secret != "") {
				t.Fatalf("wrong outbound auth metadata: %+v, scheme %q", got, after.AuthorizationScheme)
			}
			for name, want := range map[string]string{"Content-Type": "application/json", "User-Agent": "fixture-agent/1.0", "X-Opencode-Client": "term-llm", "X-Opencode-Session": "[redacted]"} {
				if got := debugTestHeader(after, name).Value; got != want {
					t.Errorf("outbound %s: want %q, got %q", name, want, got)
				}
			}
			if response.Status != http.StatusUnauthorized || debugTestHeader(response, "Www-Authenticate").Value != "[redacted]" || debugTestHeader(response, "Set-Cookie").Value != "[redacted]" {
				t.Fatalf("wrong upstream response metadata: %+v", response)
			}
		})
	}
}

func debugTestHeader(summary debugheaders.Summary, name string) debugheaders.Field {
	for _, h := range summary.Fields {
		if h.Name == name {
			return h
		}
	}
	return debugheaders.Field{}
}
