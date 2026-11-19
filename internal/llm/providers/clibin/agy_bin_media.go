package clibin

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/agyproxy"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/mcphttp"
)

// formatToolResultForAgy forwards both text and supported image blocks from
// term-llm tools (such as view_image or spawn_script image captures) over the
// agy MCP bridge.
func (p *AgyBinProvider) formatToolResultForAgy(output core.ToolOutput) mcphttp.ToolResult {
	text := p.formatToolOutput(output)
	images := mcpImageContentParts(output)
	if len(images) == 0 {
		return mcphttp.ToolResult{Content: text}
	}
	parts := make([]mcphttp.ContentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, mcphttp.ContentPart{Type: mcphttp.ContentPartText, Text: text})
	}
	parts = append(parts, images...)
	return mcphttp.ToolResult{Content: text, Parts: parts}
}

func agyMessagesHaveImages(messages []core.Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == core.PartImage {
				return true
			}
			if part.Type == core.PartToolResult && part.ToolResult != nil && core.ToolResultHasImageData(part.ToolResult) {
				return true
			}
		}
	}
	return false
}

func agyResolveImageData(imagePath string, imageData *core.ToolImageData) (string, string, bool) {
	mediaType, b64 := "", ""
	switch {
	case imageData != nil && strings.TrimSpace(imageData.Base64) != "":
		mediaType = strings.TrimSpace(imageData.MediaType)
		if mediaType == "" && strings.TrimSpace(imagePath) != "" {
			mediaType = mediaTypeFromPath(imagePath)
		}
		b64 = strings.TrimSpace(imageData.Base64)
	case strings.TrimSpace(imagePath) != "":
		raw, ok := readHydratableImagePath(imagePath)
		if !ok {
			return "", "", false
		}
		mediaType = mediaTypeFromPath(imagePath)
		b64 = base64.StdEncoding.EncodeToString(raw)
	}
	if mediaType == "" || b64 == "" {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
		return "", "", false
	}
	return mediaType, b64, true
}

type agyPromptPartBuilder struct {
	parts         []agyproxy.PromptPart
	hasInlineData bool
}

func (b *agyPromptPartBuilder) addText(s string) {
	if s == "" {
		return
	}
	if n := len(b.parts); n > 0 && b.parts[n-1].InlineData == nil {
		b.parts[n-1].Text += s
		return
	}
	b.parts = append(b.parts, agyproxy.PromptPart{Text: s})
}

func (b *agyPromptPartBuilder) addImage(mimeType, b64 string) {
	b.parts = append(b.parts, agyproxy.PromptPart{
		InlineData: &agyproxy.PromptInlineData{MIMEType: mimeType, Data: b64},
	})
	b.hasInlineData = true
}

func (b *agyPromptPartBuilder) addSectionSeparator() {
	if len(b.parts) > 0 {
		b.addText("\n\n")
	}
}

// buildAgyPromptParts builds the plain-text fallback prompt alongside ordered
// multimodal parts when the turn includes images. Text-only turns return nil
// parts so they continue using the zero-overhead plain prompt path.
func buildAgyPromptParts(messages []core.Message, workspaceDir string) (string, []agyproxy.PromptPart) {
	fallback := buildAgyPrompt(messages, workspaceDir)
	if !agyMessagesHaveImages(messages) {
		return fallback, nil
	}
	var b agyPromptPartBuilder
	if workspaceDir != "" {
		b.addText("<workspace>\nThe user's workspace is " + workspaceDir + ". Resolve relative file paths against this workspace.\n</workspace>")
	}
	if system := extractSystemPrompt(messages); system != "" {
		b.addSectionSeparator()
		b.addText("<system>\n" + system + "\n</system>")
	}
	appendAgyConversationSection(&b, messages)
	if !b.hasInlineData {
		return fallback, nil
	}
	return fallback, b.parts
}

func appendAgyConversationSection(b *agyPromptPartBuilder, messages []core.Message) {
	finalTurnStart := conversationFinalTurnStart(messages)
	if finalTurnStart <= 0 || !messagesContainPriorAssistantTurn(messages[:finalTurnStart]) {
		var body agyPromptPartBuilder
		appendAgyConversationBlocks(&body, messages)
		appendBuilderSection(b, body)
		return
	}
	var history, final agyPromptPartBuilder
	appendAgyConversationBlocks(&history, messages[:finalTurnStart])
	appendAgyConversationBlocks(&final, messages[finalTurnStart:])
	switch {
	case len(history.parts) == 0:
		appendBuilderSection(b, final)
	case len(final.parts) == 0:
		appendBuilderSection(b, history)
	default:
		b.addSectionSeparator()
		b.addText("<conversation_history>\n")
		appendBuilderInline(b, history)
		b.addText("\n</conversation_history>\n\n" + cliBinResumeReplayInstruction + "\n\n")
		appendBuilderInline(b, final)
	}
}

func appendBuilderSection(dst *agyPromptPartBuilder, src agyPromptPartBuilder) {
	if len(src.parts) == 0 {
		return
	}
	dst.addSectionSeparator()
	appendBuilderInline(dst, src)
}

func appendBuilderInline(dst *agyPromptPartBuilder, src agyPromptPartBuilder) {
	for _, p := range src.parts {
		if p.InlineData != nil {
			dst.addImage(p.InlineData.MIMEType, p.InlineData.Data)
		} else {
			dst.addText(p.Text)
		}
	}
}

func appendAgyConversationBlocks(b *agyPromptPartBuilder, messages []core.Message) {
	var pendingDeveloper string
	for _, message := range messages {
		switch message.Role {
		case core.RoleDeveloper:
			if text := core.CollectTextParts(message.Parts); text != "" {
				if pendingDeveloper != "" {
					pendingDeveloper += "\n\n"
				}
				pendingDeveloper += text
			}
		case core.RoleUser:
			appendAgyUserBlock(b, message.Parts, &pendingDeveloper)
		case core.RoleAssistant:
			appendAgyAssistantBlock(b, message.Parts)
		case core.RoleTool:
			appendAgyToolBlock(b, message.Parts)
		}
	}
}

func appendAgyUserBlock(b *agyPromptPartBuilder, parts []core.Part, pendingDeveloper *string) {
	var entry agyPromptPartBuilder
	count := 0
	sep := func() {
		if count > 0 {
			entry.addText("\n")
		}
		count++
	}
	if *pendingDeveloper != "" {
		sep()
		entry.addText("<developer>\n" + *pendingDeveloper + "\n</developer>")
		*pendingDeveloper = ""
	}
	for _, part := range parts {
		switch part.Type {
		case core.PartText, core.PartFile:
			if part.Text != "" {
				sep()
				entry.addText(part.Text)
			}
		case core.PartImage:
			sep()
			if mimeType, b64, ok := agyResolveImageData(part.ImagePath, part.ImageData); ok {
				entry.addImage(mimeType, b64)
			} else {
				entry.addText("[image]")
			}
		}
	}
	if count == 0 {
		return
	}
	b.addSectionSeparator()
	b.addText("User: ")
	appendBuilderInline(b, entry)
}

func appendAgyAssistantBlock(b *agyPromptPartBuilder, parts []core.Part) {
	if text := core.CollectTextParts(parts); text != "" {
		b.addSectionSeparator()
		b.addText("Assistant: " + text)
	}
	for _, part := range parts {
		if part.Type == core.PartToolCall && part.ToolCall != nil {
			b.addSectionSeparator()
			b.addText("Assistant called tool: " + part.ToolCall.Name)
		}
	}
}

func appendAgyToolBlock(b *agyPromptPartBuilder, parts []core.Part) {
	for _, part := range parts {
		if part.Type != core.PartToolResult || part.ToolResult == nil {
			continue
		}
		b.addSectionSeparator()
		b.addText(fmt.Sprintf("Tool result (%s): %s", part.ToolResult.Name, core.ToolResultTextContent(part.ToolResult)))
		for _, contentPart := range core.ToolResultContentParts(part.ToolResult) {
			if mimeType, b64, ok := core.ToolResultImageData(contentPart); ok {
				b.addText("\n")
				b.addImage(mimeType, b64)
			}
		}
	}
}
