package inspector

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/terminaltext"
	"github.com/term-llm/term-llm/internal/tools"
)

// Script-family results are the one shape where Content is not the user-facing
// output: spawn_script, wait_script and cancel_script record the ordered
// emissions in ContentParts and keep only the machine-readable status envelope
// in Content. Every other tool keeps the plain stored content.
var scriptResultToolNames = map[string]struct{}{
	tools.CodeExecToolName:   {},
	tools.CodeWaitToolName:   {},
	tools.CodeCancelToolName: {},
}

const (
	scriptOutputHeader = "Output:"
	scriptStatusHeader = "Status:"
)

// scriptStatusHints is the subset of a recorded status envelope that reports
// output the engine did not retain in the visible parts. The fields are shared
// by the asynchronous lifecycle envelope and by projection fallbacks.
type scriptStatusHints struct {
	OutputTruncated bool `json:"output_truncated"`
	OutputLimited   bool `json:"output_limited"`
	HasMoreOutput   bool `json:"has_more_output"`
	OmittedOutput   bool `json:"omitted_output"`
	OmittedBytes    int  `json:"output_omitted_bytes"`
	OmittedImages   int  `json:"output_omitted_images"`
}

// outputNotRetained reports whether the status says output existed but no
// visible part carries it, so the inspector must not claim there was none.
func (h scriptStatusHints) outputNotRetained() bool {
	return h.OutputTruncated || h.OutputLimited || h.HasMoreOutput || h.OmittedOutput ||
		h.OmittedBytes > 0 || h.OmittedImages > 0
}

// inspectorToolResultContent returns the body shown inside a tool-result box.
// Ordered script output is reconstructed only for the exact script family with
// recorded parts; everything else returns the stored Content unchanged so plain
// tool results render exactly as before.
func inspectorToolResultContent(tr *core.ToolResult) string {
	if tr == nil {
		return ""
	}
	if _, ok := scriptResultToolNames[tr.Name]; !ok {
		return tr.Content
	}
	if len(tr.ContentParts) == 0 {
		// Script results always carry the envelope, so escape it like any other
		// string this package introduces; other tools stay byte-identical.
		return scriptSafeText(tr.Content)
	}

	output := strings.TrimRight(scriptResultOutput(tr.ContentParts, tr.Content), "\n")
	status := ""
	if tr.Content != "" {
		status = scriptSafeText(tr.Content)
	}

	var b strings.Builder
	b.WriteString(scriptOutputHeader)
	switch {
	case output == "":
		b.WriteString(" (")
		b.WriteString(scriptEmptyOutputLabel(tr.Content))
		b.WriteString(")")
	case strings.HasPrefix(output, "\n"):
		b.WriteString(output)
	default:
		b.WriteString("\n")
		b.WriteString(output)
	}
	if status != "" {
		b.WriteString("\n\n")
		b.WriteString(scriptStatusHeader)
		b.WriteString("\n")
		b.WriteString(status)
	}
	return b.String()
}

// scriptEmptyOutputLabel distinguishes a genuinely empty observation from one
// whose output the engine omitted or truncated before it reached the session.
func scriptEmptyOutputLabel(content string) string {
	var hints scriptStatusHints
	if json.Unmarshal([]byte(content), &hints) == nil && hints.outputNotRetained() {
		return "no retained output"
	}
	return "no recorded output"
}

// scriptResultOutput replays the ordered recorded emissions. The script tools
// append the persisted status envelope as one final text part ("\n"+Content, or
// Content alone after projection); that part is dropped here because the
// envelope is rendered as its own section. Consecutive text parts concatenate
// exactly as recorded, identical JSON the script itself emitted earlier is
// preserved, and only image/unsupported placeholders get line separators.
func scriptResultOutput(parts []core.ToolContentPart, envelope string) string {
	var b strings.Builder
	written := false
	endsWithNewline := false
	afterPlaceholder := false

	for i, part := range parts {
		if i == len(parts)-1 && envelope != "" && part.Type == core.ToolContentPartText &&
			(part.Text == envelope || part.Text == "\n"+envelope) {
			continue
		}
		var piece string
		placeholder := false
		switch part.Type {
		case core.ToolContentPartText:
			piece = scriptSafeText(part.Text)
		case core.ToolContentPartImageData:
			piece, placeholder = scriptImagePlaceholder(part.ImageData), true
		default:
			piece, placeholder = scriptUnsupportedPartPlaceholder(part.Type), true
		}
		switch {
		case piece == "":
			continue
		case placeholder && written && !endsWithNewline:
			b.WriteString("\n")
		case !placeholder && afterPlaceholder && !strings.HasPrefix(piece, "\n"):
			b.WriteString("\n")
		}
		b.WriteString(piece)
		written = true
		endsWithNewline = strings.HasSuffix(piece, "\n")
		afterPlaceholder = placeholder && !endsWithNewline
	}

	return b.String()
}

// scriptImagePlaceholder names media that the text-only inspector cannot draw.
// The media type is untrusted, so it is escaped onto one line.
func scriptImagePlaceholder(data *core.ToolImageData) string {
	if data == nil {
		return "[Image]"
	}
	mediaType := scriptSafeLine(data.MediaType)
	if mediaType == "" {
		return fmt.Sprintf("[Image: %d base64 chars]", len(data.Base64))
	}
	return fmt.Sprintf("[Image: %s, %d base64 chars]", mediaType, len(data.Base64))
}

// formatToolCallArgs keeps full recorded script source safe in the inspector,
// including code beyond the inline preview's byte/line bounds.
func (r *ContentRenderer) formatToolCallArgs(tc *core.ToolCall) string {
	args := r.formatJSON(tc.Arguments)
	if _, script := scriptResultToolNames[tc.Name]; script {
		return scriptSafeText(args)
	}
	return args
}

// wrapToolResultLine keeps script-family call and result bodies inside the box.
// wrapLine only breaks at whitespace, so compact JSON status envelopes and
// unbroken CJK runs would overflow; family text is hard-wrapped by cell width
// instead. Other tools keep the whitespace-only wrapper they have always used.
func wrapToolResultLine(name, line string, maxWidth int) []string {
	if _, ok := scriptResultToolNames[name]; !ok {
		return wrapLine(line, maxWidth)
	}
	// Match wrapLine's tab handling: terminals expand tabs, width counts one.
	line = strings.ReplaceAll(line, "\t", "  ")
	if maxWidth <= 0 || xansi.StringWidth(line) <= maxWidth {
		return []string{line}
	}
	return strings.Split(xansi.Hardwrap(line, maxWidth, true), "\n")
}

// scriptUnsupportedPartPlaceholder names a content part this text surface
// cannot draw without leaking its payload.
func scriptUnsupportedPartPlaceholder(partType core.ToolContentPartType) string {
	name := scriptSafeLine(string(partType))
	if name == "" {
		return "[Unsupported output part]"
	}
	return fmt.Sprintf("[Unsupported output part: %s]", name)
}

// scriptSafeLine escapes one line of untrusted script metadata.
func scriptSafeLine(s string) string {
	return escapeScriptFormat(terminaltext.EscapeControls(s))
}

// scriptSafeText escapes multi-line untrusted script text while keeping its
// line structure.
func scriptSafeText(s string) string {
	return escapeScriptFormat(terminaltext.EscapeControlsPreserveLayout(s))
}

// escapeScriptFormat renders Unicode format characters (bidi overrides in
// particular) as visible \uXXXX/\UXXXXXXXX escapes so they cannot reorder the
// inspector while the exact recorded value stays recoverable.
func escapeScriptFormat(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if !unicode.Is(unicode.Cf, r) {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			fmt.Fprintf(&b, `\U%08x`, r)
		} else {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
