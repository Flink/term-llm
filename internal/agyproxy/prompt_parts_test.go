package agyproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodePartsForUserTurn(t *testing.T, body []byte, turnIndex int) []map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	contents := root["request"].(map[string]any)["contents"].([]any)
	rawParts := contents[turnIndex].(map[string]any)["parts"].([]any)
	out := make([]map[string]any, 0, len(rawParts))
	for _, raw := range rawParts {
		out = append(out, raw.(map[string]any))
	}
	return out
}

func TestStagePromptPartsRoundTripAndMetadata(t *testing.T) {
	home := t.TempDir()
	pngB64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake-png"))
	parts := []PromptPart{
		{Text: "User: "},
		{InlineData: &PromptInlineData{MIMEType: "image/png", Data: pngB64}},
		{Text: "\nwhat is going on here"},
	}
	wire, err := StagePromptParts(home, parts, "User: [image]\nwhat is going on here")
	if err != nil {
		t.Fatalf("StagePromptParts: %v", err)
	}

	t.Run("unwrapped", func(t *testing.T) {
		out, _, err := processGenerationRequestWithPrompts(promptBody(wire), "", home, false, maxRequestBody)
		if err != nil {
			t.Fatalf("processGenerationRequestWithPrompts: %v", err)
		}
		got := decodePartsForUserTurn(t, out, 0)
		if len(got) != 3 {
			t.Fatalf("parts count = %d, want 3: %#v", len(got), got)
		}
		if got[0]["text"] != "User: " {
			t.Fatalf("part[0] = %#v", got[0])
		}
		inline := got[1]["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/png" || inline["data"] != pngB64 {
			t.Fatalf("part[1] inlineData = %#v", inline)
		}
		if got[2]["text"] != "\nwhat is going on here" {
			t.Fatalf("part[2] = %#v", got[2])
		}
	})

	t.Run("wrapped with USER_REQUEST and metadata", func(t *testing.T) {
		metadata := "\n<ADDITIONAL_METADATA>\ntime=now\n</ADDITIONAL_METADATA>"
		wrapped := userRequestOpen + wire + userRequestClose + metadata
		out, _, err := processGenerationRequestWithPrompts(promptBody(wrapped), "", home, false, maxRequestBody)
		if err != nil {
			t.Fatalf("processGenerationRequestWithPrompts: %v", err)
		}
		got := decodePartsForUserTurn(t, out, 0)
		if len(got) != 3 {
			t.Fatalf("parts count = %d, want 3: %#v", len(got), got)
		}
		if got[0]["text"] != userRequestOpen+"User: " {
			t.Fatalf("part[0] = %#v", got[0])
		}
		inline := got[1]["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/png" || inline["data"] != pngB64 {
			t.Fatalf("part[1] inlineData = %#v", inline)
		}
		if got[2]["text"] != "\nwhat is going on here"+userRequestClose+metadata {
			t.Fatalf("part[2] = %#v", got[2])
		}
	})

	t.Run("truncated fallback still restores full multimodal parts", func(t *testing.T) {
		longFallback := strings.Repeat("context line\n", 100)
		longWire, err := StagePromptParts(home, parts, longFallback)
		if err != nil {
			t.Fatal(err)
		}
		wrapped := userRequestOpen + longWire + userRequestClose
		truncated := wrapped[:160] + "\n<truncated 500 bytes>"
		out, _, err := processGenerationRequestWithPrompts(promptBody(truncated), "", home, false, maxRequestBody)
		if err != nil {
			t.Fatalf("processGenerationRequestWithPrompts truncated: %v", err)
		}
		got := decodePartsForUserTurn(t, out, 0)
		if len(got) != 3 {
			t.Fatalf("parts count = %d, want 3: %#v", len(got), got)
		}
		inline := got[1]["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/png" || inline["data"] != pngB64 {
			t.Fatalf("inlineData = %#v", inline)
		}
	})

	t.Run("image-only prompt", func(t *testing.T) {
		onlyWire, err := StagePromptParts(home, []PromptPart{{InlineData: &PromptInlineData{MIMEType: "image/jpeg", Data: pngB64}}}, "")
		if err != nil {
			t.Fatal(err)
		}
		wrapped := userRequestOpen + onlyWire + userRequestClose
		out, _, err := processGenerationRequestWithPrompts(promptBody(wrapped), "", home, false, maxRequestBody)
		if err != nil {
			t.Fatal(err)
		}
		got := decodePartsForUserTurn(t, out, 0)
		if len(got) != 3 || got[0]["text"] != userRequestOpen || got[2]["text"] != userRequestClose {
			t.Fatalf("image-only expanded = %#v", got)
		}
		inline := got[1]["inlineData"].(map[string]any)
		if inline["mimeType"] != "image/jpeg" || inline["data"] != pngB64 {
			t.Fatalf("inlineData = %#v", inline)
		}
	})

	t.Run("rehydrates historical image turns on native resume", func(t *testing.T) {
		body := promptBody(userRequestOpen+wire+userRequestClose, userRequestOpen+"follow up question"+userRequestClose)
		out, _, err := processGenerationRequestWithPrompts(body, "", home, false, maxRequestBody)
		if err != nil {
			t.Fatal(err)
		}
		first := decodePartsForUserTurn(t, out, 0)
		second := decodePartsForUserTurn(t, out, 1)
		if len(first) != 3 || first[1]["inlineData"] == nil {
			t.Fatalf("turn 0 lost inlineData on resume: %#v", first)
		}
		if len(second) != 1 || second[0]["text"] != userRequestOpen+"follow up question"+userRequestClose {
			t.Fatalf("turn 1 unexpected: %#v", second)
		}
	})
}

func TestStagePromptPartsIgnoresUntrustedMarkersAndFailsClosed(t *testing.T) {
	home := t.TempDir()
	pngB64 := base64.StdEncoding.EncodeToString([]byte("png-data"))
	parts := []PromptPart{{InlineData: &PromptInlineData{MIMEType: "image/png", Data: pngB64}}}
	wire, err := StagePromptParts(home, parts, "fallback")
	if err != nil {
		t.Fatal(err)
	}

	// Quoted user prose and model output must never expand.
	for _, text := range []string{"quoted: " + wire, "prefix\n" + wire} {
		body := promptBody(text)
		out, _, err := processGenerationRequestWithPrompts(body, "", home, false, maxRequestBody)
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("quoted text changed: %q err=%v", text, err)
		}
	}
	modelBody, _ := json.Marshal(map[string]any{"request": map[string]any{
		"contents": []any{map[string]any{"role": "model", "parts": []any{map[string]any{"text": wire}}}},
	}})
	out, _, err := processGenerationRequestWithPrompts(modelBody, "", home, false, maxRequestBody)
	if err != nil || !bytes.Equal(out, modelBody) {
		t.Fatalf("model text changed: err=%v", err)
	}

	// Corrupt staged file or mismatched hash must fail closed with 502.
	encoded, _ := json.Marshal(stagedPromptPartsPayload{Fallback: "fallback", Parts: parts})
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	stagedFile := filepath.Join(home, promptDirectory, hash+".json")

	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{"bad hash", func(s string) string { return strings.Replace(s, hash, strings.Repeat("0", 64), 1) }},
		{"path traversal", func(string) string {
			return `<term_llm_prompt_parts sha256="../../outside">` + "\nx\n</term_llm_prompt_parts>"
		}},
		{"tampered fallback", func(s string) string { return strings.Replace(s, "fallback", "tampered", 1) }},
		{"corrupt file", func(s string) string { _ = os.WriteFile(stagedFile, []byte("corrupt"), 0o600); return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.mutate(userRequestOpen + wire + userRequestClose)
			server := &Server{transport: &http.Transport{}, promptRoot: home}
			rec := httptest.NewRecorder()
			server.forward(rec, httptest.NewRequest(http.MethodPost, "https://"+CloudCodeHost+generationPath, bytes.NewReader(promptBody(text))))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestStagePromptPartsRejectsInvalidInputs(t *testing.T) {
	home := t.TempDir()
	if _, err := StagePromptParts(home, nil, "x"); err == nil {
		t.Fatal("accepted empty parts")
	}
	if _, err := StagePromptParts(home, []PromptPart{{InlineData: &PromptInlineData{MIMEType: "image/png", Data: "not-valid-base64!!!"}}}, "x"); err == nil {
		t.Fatal("accepted invalid base64")
	}
	if _, err := StagePromptParts(home, []PromptPart{{InlineData: &PromptInlineData{MIMEType: "", Data: "AAAA"}}}, "x"); err == nil {
		t.Fatal("accepted empty mimeType")
	}
}
