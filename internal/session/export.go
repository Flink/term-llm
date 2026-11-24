package session

import (
	"fmt"
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/formatutil"
	"github.com/term-llm/term-llm/internal/llm/core"
	internalreasoning "github.com/term-llm/term-llm/internal/reasoning"
)

// ExportOptions configures session export.
type ExportOptions struct {
	IncludeSystem             bool // Include system prompt in export
	IncludeReasoningSummaries bool // Include provider-sanctioned reasoning summaries
	IncludeRawReasoning       bool // Include raw reasoning; caller must enforce safety gate
	Partial                   bool // Export is a transcript prefix; omit misleading whole-session metrics
	IncludeAudio              bool // Explicit opt-in; inline HTML only, metadata copied as-is
	ResponseOnly              bool // Export is a standalone assistant response

	// Images selects inline data URIs (default), separate assets (share
	// bundles only), or omission. AssetMediaTypes lists the asset encodings the
	// destination accepts.
	Images          ExportImageMode
	AssetMediaTypes []string
	// Media supplies additional term-llm-media:// artifacts for resolving
	// references when the selected messages omit the producing tool results,
	// as in response-only shares.
	Media []core.MediaArtifact
}

// escapeTableCell escapes special characters for markdown table cells.
func escapeTableCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

// ExportToMarkdown exports a session and its messages to a pretty markdown format.
func ExportToMarkdown(sess *Session, messages []Message, opts ExportOptions) string {
	return exportToMarkdown(sess, messages, opts, nil)
}

// exportToMarkdown renders Markdown. Share bundles pass images: in asset mode,
// session images and term-llm-media:// references become the same bundle
// assets used by the HTML rendering; otherwise references become textual
// placeholders and images stay in the HTML only. A nil images keeps the
// historical text-only local export.
func exportToMarkdown(sess *Session, messages []Message, opts ExportOptions, images *exportImages) string {
	var b strings.Builder
	messages = VisibleExportMessages(messages)
	mediaReferences := htmlMediaReferences(messages, opts.Media)
	assets := images.assetsMode()
	rewriteText := func(text string) string {
		if images == nil {
			return replaceMarkdownPresentationReferences(text, mediaReferences)
		}
		if !assets {
			return replaceHTMLMediaReferences(text, mediaReferences, nil)
		}
		return replaceHTMLMediaReferences(text, mediaReferences, images)
	}

	title := sess.PreferredShortTitle()
	if title == "" {
		title = ShortID(sess.ID)
	}
	heading := "Session"
	if opts.ResponseOnly {
		heading = "Assistant response"
	}
	fmt.Fprintf(&b, "# %s: %s\n\n", heading, escapeTableCell(title))

	b.WriteString("> Exported from [term-llm](https://github.com/term-llm/term-llm)\n\n")

	if !opts.ResponseOnly {
		b.WriteString("## Setup\n\n| | |\n|---|---|\n")
		if sess.Agent != "" {
			b.WriteString(fmt.Sprintf("| **Agent** | %s |\n", escapeTableCell(sess.Agent)))
		}
		fmt.Fprintf(&b, "| **Provider** | %s |\n| **Model** | %s |\n", escapeTableCell(sess.Provider), escapeTableCell(sess.Model))

		mode := string(sess.Mode)
		if mode == "" {
			mode = "chat"
		}
		fmt.Fprintf(&b, "| **Mode** | %s |\n| **Created** | %s |\n", mode, sess.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
		if sess.CWD != "" {
			b.WriteString(fmt.Sprintf("| **Working Directory** | `%s` |\n", escapeTableCell(sess.CWD)))
		}

		b.WriteString("\n")

		if sess.Tools != "" {
			fmt.Fprintf(&b, "<details>\n<summary>Tools</summary>\n\n%s\n\n</details>\n\n", sess.Tools)
		}
		if sess.MCP != "" {
			fmt.Fprintf(&b, "<details>\n<summary>MCP Servers</summary>\n\n%s\n\n</details>\n\n", sess.MCP)
		}
	}

	if !opts.Partial && !opts.ResponseOnly {
		fmt.Fprintf(&b, "## Metrics\n\n| Turns | Tool Calls | Tokens |\n|-------|------------|--------|\n| %d user / %d LLM | %d | %s |\n\n",
			sess.UserTurns, sess.LLMTurns, sess.ToolCalls, formatTokens(sess.InputTokens, sess.OutputTokens))
	}

	if !opts.ResponseOnly {
		b.WriteString("---\n\n## Conversation\n\n")
	}

	// Pair calls with results as they arrive.
	toolCalls := make(map[string]*core.ToolCall)
	var pendingToolCalls []*core.ToolCall
	inAssistantTurn := false

	flushAssistant := func() {
		if !inAssistantTurn {
			return
		}
		// Write any orphan tool calls (no results received)
		for _, tc := range pendingToolCalls {
			if _, exists := toolCalls[tc.ID]; exists {
				writeToolCall(&b, tc, nil, nil)
				delete(toolCalls, tc.ID)
			}
		}
		pendingToolCalls = nil
		b.WriteString("---\n\n")
		inAssistantTurn = false
	}

	for _, msg := range messages {
		if msg.Role == core.RoleSystem {
			if opts.IncludeSystem {
				flushAssistant()
				fmt.Fprintf(&b, "### System\n\n%s\n\n---\n\n", replaceMarkdownPresentationReferences(msg.TextContent, mediaReferences))
			}
			continue
		}

		if msg.Role == core.RoleUser {
			flushAssistant()
			fmt.Fprintf(&b, "### User\n\n%s\n\n", replaceMarkdownPresentationReferences(msg.TextContent, mediaReferences))
			if assets {
				for _, part := range msg.Parts {
					if part.Type == core.PartImage {
						writeMarkdownExportImage(&b, images, partImageSource(part, "Attached image"))
					}
				}
			}
			b.WriteString("---\n\n")
			continue
		}

		if msg.Role == core.RoleAssistant {
			flushAssistant()
			inAssistantTurn = true
			b.WriteString("### Assistant\n\n")

			for _, part := range msg.Parts {
				if part.Type == core.PartText {
					if rendered := RenderExportReasoning(part, opts); rendered != "" {
						b.WriteString(rendered)
					}
					if part.Text != "" {
						b.WriteString(rewriteText(part.Text))
						b.WriteString("\n\n")
					}
				}
				if part.Type == core.PartImage && assets {
					writeMarkdownExportImage(&b, images, partImageSource(part, "Image"))
				}
				if part.Type == core.PartToolCall && part.ToolCall != nil {
					pendingToolCalls = append(pendingToolCalls, part.ToolCall)
					toolCalls[part.ToolCall.ID] = part.ToolCall
				}
			}
			continue
		}

		if msg.Role == core.RoleTool {

			for _, part := range msg.Parts {
				result := part.ToolResult
				if part.Type != core.PartToolResult || result == nil {
					continue
				}
				call := toolCalls[result.ID]
				writeToolCall(&b, call, result, images)
				delete(toolCalls, result.ID)
				if assets {
					for _, source := range toolResultDisplayedImages(result, true) {
						writeMarkdownExportImage(&b, images, source)
					}
				}
			}
		}
	}

	flushAssistant()

	return b.String()
}

// RenderExportReasoning renders non-encrypted reasoning metadata according to
// explicit export options. Raw reasoning is never included unless requested.
func RenderExportReasoning(part core.Part, opts ExportOptions) string {
	display := core.ReasoningForDisplay(part)
	if display == nil {
		return ""
	}
	content := display.Content
	if content == "" {
		content = strings.Join(display.SummaryParts, "\n\n")
	}
	if strings.TrimSpace(content) == "" {
		return ""
	}
	switch core.NormalizeStoredReasoningKind(display.Kind, display.Content != "" || len(display.SummaryParts) > 0) {
	case core.ReasoningKindSummary:
		if !opts.IncludeReasoningSummaries {
			return ""
		}
		parsed := internalreasoning.ParseReasoningSummary(content)
		title := strings.TrimSpace(display.SummaryTitle)
		if title == "" {
			title = parsed.Title
		}
		body := parsed.Body
		if strings.TrimSpace(body) == "" {
			body = strings.TrimSpace(content)
		}
		if title != "" {
			return fmt.Sprintf("_Reasoning summary: %s_\n\n%s\n\n", title, strings.TrimSpace(body))
		}
		return fmt.Sprintf("_Reasoning summary:_\n\n%s\n\n", strings.TrimSpace(body))
	case core.ReasoningKindRaw:
		if !opts.IncludeRawReasoning {
			return ""
		}
		return fmt.Sprintf("_Raw reasoning (explicitly included):_\n\n%s\n\n", strings.TrimSpace(content))
	default:
		return ""
	}
}

// writeToolCall writes a tool call with optional result in a collapsible block.
func writeToolCall(b *strings.Builder, tc *core.ToolCall, result *core.ToolResult, images *exportImages) {
	b.WriteString("<details>\n")
	if tc != nil {
		fmt.Fprintf(b, "<summary>%s</summary>\n\n", tc.Name)
		writeMarkdownToolCode(b, prettyJSON(tc.Arguments), "json")
	} else {
		fmt.Fprintf(b, "<summary>%s (result)</summary>\n\n", result.Name)
	}
	if result != nil {
		writeMarkdownToolResult(b, result, images)
	}
	b.WriteString("</details>\n\n")
}

// formatTokens formats input/output tokens in a readable format.
func formatTokens(input, output int) string {
	if input == 0 && output == 0 {
		return "-"
	}
	return fmt.Sprintf("%s in / %s out", formatCount(input), formatCount(output))
}

// formatCount keeps exported transcripts' uppercase K style while using the
// same compact token formatter as terminal surfaces.
func formatCount(n int) string {
	return strings.Replace(formatutil.FormatTokens(int64(n)), "k", "K", 1)
}

// Structured text is authoritative when present. In particular, scripts
// already put their scalar receipt at the end of the ordered parts. Keep the
// scalar fallback for legacy and image-only results without structured text.
func exportToolResultParts(result *core.ToolResult) []core.ToolContentPart {
	parts := result.ContentParts
	if result.Content != "" && !slices.ContainsFunc(parts, func(part core.ToolContentPart) bool {
		return part.Type == core.ToolContentPartText && part.Text != ""
	}) {
		return append([]core.ToolContentPart{{Type: core.ToolContentPartText, Text: result.Content}}, parts...)
	}
	return parts
}

func toolContentImageSource(part core.ToolContentPart) exportImageSource {
	source := exportImageSource{Alt: "Tool image"}
	if part.ImageData != nil {
		source.MediaType = part.ImageData.MediaType
		source.Base64 = part.ImageData.Base64
	}
	return source
}

func writeMarkdownToolResult(b *strings.Builder, result *core.ToolResult, images *exportImages) {
	if result.IsError {
		b.WriteString("**Error:**\n")
	} else {
		b.WriteString("**Result:**\n")
	}
	for _, part := range exportToolResultParts(result) {
		switch part.Type {
		case core.ToolContentPartText:
			if part.Text != "" {
				writeMarkdownToolCode(b, part.Text, "")
			}
		case core.ToolContentPartImageData:
			if images.assetsMode() {
				writeMarkdownExportImage(b, images, toolContentImageSource(part))
			} else {
				b.WriteString("\n_Tool image omitted._\n\n")
			}
		}
	}
}

// Emitted text may contain Markdown fences. Keep it inside the code block.
func writeMarkdownToolCode(b *strings.Builder, text, language string) {
	longest, run := 2, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	fmt.Fprintf(b, "%s%s\n%s\n%s\n\n", fence, language, text, fence)
}

func writeMarkdownExportImage(target *strings.Builder, images *exportImages, source exportImageSource) {
	image := buildHTMLImage(images, source)
	if image.Omitted {
		fmt.Fprintf(target, "_%s omitted — %s._\n\n", escapeTableCell(image.Alt), image.Reason)
		return
	}
	fmt.Fprintf(target, "![%s](%s)\n\n", escapeMarkdownImageAlt(image.Alt), image.URL)
}
