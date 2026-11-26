package agyproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const availableModelsPath = "/v1internal:fetchAvailableModels"

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestServerObservesFetchAvailableModelsCatalog(t *testing.T) {
	catalog := `{
		"models": {
			"gemini-4-neon-high": {"model": "MODEL_PLACEHOLDER_M532", "maxTokens": 1048576, "maxOutputTokens": 262144},
			"gemini-4-neon-low":  {"model": "MODEL_PLACEHOLDER_M534", "maxTokens": 1048576, "maxOutputTokens": 262144},
			"claude-opus-5-5-high": {"model": "MODEL_PLACEHOLDER_M402", "maxTokens": 1000000, "maxOutputTokens": 128000},
			"gpt-oss-120b-medium": {"model": "MODEL_OPENAI_GPT_OSS_120B_MEDIUM", "maxTokens": 131072}
		}
	}`

	for _, tc := range []struct {
		name string
		gzip bool
	}{
		{name: "plain", gzip: false},
		{name: "gzip", gzip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server Server
			if _, _, err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Stop(context.Background()) })

			raw := []byte(catalog)
			headers := http.Header{"Content-Type": []string{"application/json"}}
			if tc.gzip {
				raw = gzipBytes(t, raw)
				headers.Set("Content-Encoding", "gzip")
			}

			server.mu.Lock()
			transport := server.transport
			server.mu.Unlock()
			transport.RegisterProtocol("https", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     headers.Clone(),
					Body:       io.NopCloser(bytes.NewReader(raw)),
				}, nil
			}))

			req := httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+availableModelsPath, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			server.forward(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), raw) {
				t.Fatal("proxied fetchAvailableModels response bytes were altered")
			}

			for model, want := range map[string]int{
				"gemini-4-neon-high":               1048576,
				"MODEL_PLACEHOLDER_M532":           1048576,
				"gemini-4-neon":                    0, // the proxy only matches exact IDs
				"claude-opus-5-5-high":             1000000,
				"gpt-oss-120b-medium":              131072,
				"MODEL_OPENAI_GPT_OSS_120B_MEDIUM": 131072,
				"unknown-model":                    0,
			} {
				if got := server.ModelContextWindow(model); got != want {
					t.Errorf("ModelContextWindow(%q) = %d, want %d", model, got, want)
				}
			}
		})
	}
}

func TestServerCatalogObservationNeverFailsForwarding(t *testing.T) {
	var server Server
	if _, _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	server.mu.Lock()
	transport := server.transport
	server.mu.Unlock()
	transport.RegisterProtocol("https", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(strings.NewReader("not-valid-gzip")),
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+availableModelsPath, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	server.forward(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "not-valid-gzip" {
		t.Fatalf("response = (%d, %q), want untouched (200, not-valid-gzip)", rec.Code, rec.Body.String())
	}
	if got := server.ModelContextWindow("gemini-4-neon-high"); got != 0 {
		t.Fatalf("ModelContextWindow after invalid payload = %d, want 0", got)
	}
}

func TestServerCatalogOverflowForwardsWithoutBuffering(t *testing.T) {
	var server Server
	if _, _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	oversized := bytes.Repeat([]byte("a"), maxCatalogResponse+1024)
	server.mu.Lock()
	transport := server.transport
	server.mu.Unlock()
	transport.RegisterProtocol("https", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(oversized)),
		}, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+availableModelsPath, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	server.forward(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != len(oversized) {
		t.Fatalf("oversized response = (%d, %d bytes), want (200, %d bytes)", rec.Code, rec.Body.Len(), len(oversized))
	}
	if got := server.ModelContextWindow("gemini-4-neon-high"); got != 0 {
		t.Fatalf("ModelContextWindow after overflow = %d, want 0", got)
	}
}

func TestServerTracksLastMainGenerationModel(t *testing.T) {
	t.Setenv(GenerationTraceFileEnv, "")
	var server Server
	if _, _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	server.mu.Lock()
	transport := server.transport
	server.mu.Unlock()
	transport.RegisterProtocol("https", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}))

	server.BeginTurn(true)
	mainReq := `{"model":"gemini-4-neon-high","request":{"tools":[{"functionDeclarations":[{"name":"mcp_term-llm_read_file"}]}]}}`
	rec := httptest.NewRecorder()
	server.forward(rec, httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+generationPath, strings.NewReader(mainReq)))
	if rec.Code != http.StatusOK {
		t.Fatalf("main status = %d", rec.Code)
	}
	if got := server.LastGenerationModel(); got != "gemini-4-neon-high" {
		t.Fatalf("LastGenerationModel() = %q, want gemini-4-neon-high", got)
	}

	// Auxiliary request without tools must not overwrite the main generation model.
	auxReq := `{"model":"gemini-3-flash","request":{"contents":[]}}`
	rec = httptest.NewRecorder()
	server.forward(rec, httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+generationPath, strings.NewReader(auxReq)))
	if rec.Code != http.StatusOK {
		t.Fatalf("aux status = %d", rec.Code)
	}
	if got := server.LastGenerationModel(); got != "gemini-4-neon-high" {
		t.Fatalf("LastGenerationModel() after aux = %q, want gemini-4-neon-high", got)
	}

	// BeginTurn clears the previous turn's generation model.
	server.BeginTurn(false)
	if got := server.LastGenerationModel(); got != "" {
		t.Fatalf("LastGenerationModel() after BeginTurn = %q, want empty", got)
	}
}
