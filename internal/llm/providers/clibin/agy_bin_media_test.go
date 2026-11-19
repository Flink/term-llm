package clibin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/agyproxy"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mcphttp"
)

func TestBuildAgyPromptPartsTextOnlyReturnsNil(t *testing.T) {
	msgs := []core.Message{
		core.SystemText("sys"),
		core.UserText("hello"),
	}
	fallback, parts := buildAgyPromptParts(msgs, "/repo")
	if parts != nil {
		t.Fatalf("text-only turn returned parts = %#v, want nil", parts)
	}
	if !strings.Contains(fallback, "hello") {
		t.Fatalf("fallback = %q", fallback)
	}
}

func TestBuildAgyPromptPartsUserImageAndText(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\nscreenshot-bytes")
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	msgs := []core.Message{
		core.SystemText("be concise"),
		{Role: core.RoleUser, Parts: []core.Part{
			{Type: core.PartImage, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: b64}},
			{Type: core.PartText, Text: "what is going on here"},
		}},
	}
	fallback, parts := buildAgyPromptParts(msgs, "/repo")
	if !strings.Contains(fallback, "[image]") || !strings.Contains(fallback, "what is going on here") {
		t.Fatalf("fallback = %q", fallback)
	}
	var images []*agyproxy.PromptInlineData
	var joinedText strings.Builder
	for _, part := range parts {
		if part.InlineData != nil {
			images = append(images, part.InlineData)
		} else {
			joinedText.WriteString(part.Text)
		}
	}
	if len(images) != 1 || images[0].MIMEType != "image/png" || images[0].Data != b64 {
		t.Fatalf("images = %#v, want 1 png image", images)
	}
	if !strings.Contains(joinedText.String(), "what is going on here") || !strings.Contains(joinedText.String(), "<system>\nbe concise\n</system>") {
		t.Fatalf("joined text = %q", joinedText.String())
	}
}

func TestBuildAgyPromptPartsHydratesUploadImagePathWhenBase64Stripped(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	uploadDir := filepath.Join(dataHome, "term-llm", "uploads")
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	imgBytes := []byte("persisted-png-bytes")
	imgPath := filepath.Join(uploadDir, "shot.png")
	if err := os.WriteFile(imgPath, imgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	msgs := []core.Message{{
		Role: core.RoleUser,
		Parts: []core.Part{
			{Type: core.PartImage, ImagePath: imgPath, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: ""}},
			{Type: core.PartText, Text: "check this screenshot"},
		},
	}}
	_, parts := buildAgyPromptParts(msgs, "")
	wantB64 := base64.StdEncoding.EncodeToString(imgBytes)
	found := false
	for _, part := range parts {
		if part.InlineData != nil && part.InlineData.MIMEType == "image/png" && part.InlineData.Data == wantB64 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected hydrated inlineData from %s, got parts = %#v", imgPath, parts)
	}
}

func TestBuildAgyPromptPartsIncludesToolResultImages(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("tool-png"))
	msgs := []core.Message{
		core.UserText("inspect the screenshot"),
		{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartToolCall, ToolCall: &core.ToolCall{ID: "c1", Name: "view_image"}}}},
		{Role: core.RoleTool, Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{
			ID: "c1", Name: "view_image",
			ContentParts: []core.ToolContentPart{
				{Type: core.ToolContentPartText, Text: "Image loaded"},
				{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: b64}},
			},
		}}}},
		core.UserText("what do you see?"),
	}
	_, parts := buildAgyPromptParts(msgs, "")
	found := false
	for _, part := range parts {
		if part.InlineData != nil && part.InlineData.MIMEType == "image/png" && part.InlineData.Data == b64 {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool result image missing from parts: %#v", parts)
	}
}

func TestAgyRunCommandWithPartsStagesEnvelopeForProxyExpansion(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("png-payload"))
	captureFile := filepath.Join(t.TempDir(), "stdin.ndjson")
	script := "cat > '" + captureFile + "'\n" +
		`printf '{"event":"init","conversation_id":"conv-img"}\n'` + "\n" +
		`printf '{"event":"result","result":{"conversation_id":"conv-img","status":"SUCCESS","response":"seen"}}\n'` + "\n"
	p := newAgyTransportProvider(t, writeAgyTestScript(t, script), nil)
	plan, err := p.planTurn(core.Request{
		Messages: []core.Message{{
			Role: core.RoleUser,
			Parts: []core.Part{
				{Type: core.PartImage, ImageData: &core.ToolImageData{MediaType: "image/png", Base64: b64}},
				{Type: core.PartText, Text: "describe this image"},
			},
		}},
	}, "")
	if err != nil {
		t.Fatalf("planTurn: %v", err)
	}
	if len(plan.PromptParts) == 0 {
		t.Fatal("expected non-empty PromptParts for image turn")
	}
	events := make(chan core.Event, 16)
	_, err = p.runCommandWithParts(context.Background(), p.buildArgs(core.Request{}, ""), plan.Prompt, plan.PromptParts, "", "", false, eventSender{ctx: context.Background(), ch: events}, false)
	if err != nil {
		t.Fatalf("runCommandWithParts: %v", err)
	}
	raw, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatal(err)
	}
	var env agyStreamInput
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode stdin event: %v", err)
	}
	if !strings.HasPrefix(env.Message.Content, `<term_llm_prompt_parts sha256="`) {
		t.Fatalf("stdin content = %q, want staged prompt_parts envelope", env.Message.Content)
	}
	if strings.Contains(env.Message.Content, b64) {
		t.Fatalf("stdin leaked raw base64 instead of staging it privately")
	}
}

func TestAgyFormatToolResultForwardsMCPImages(t *testing.T) {
	p := NewAgyBinProvider("", nil)
	pngBytes := []byte("fake-png")
	res := p.formatToolResultForAgy(core.ToolOutput{
		Content: "Image loaded",
		ContentParts: []core.ToolContentPart{
			{Type: core.ToolContentPartText, Text: "Image loaded"},
			{Type: core.ToolContentPartImageData, ImageData: &core.ToolImageData{
				MediaType: "image/png",
				Base64:    base64.StdEncoding.EncodeToString(pngBytes),
			}},
		},
	})
	if len(res.Parts) != 2 {
		t.Fatalf("MCP parts = %#v, want text + image", res.Parts)
	}
	if res.Parts[1].Type != mcphttp.ContentPartImage || res.Parts[1].MIMEType != "image/png" || string(res.Parts[1].Data) != string(pngBytes) {
		t.Fatalf("image part = %#v", res.Parts[1])
	}
}
