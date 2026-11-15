package ui

import (
	"sort"
	"strings"

	"github.com/term-llm/term-llm/internal/terminaltext"
	"github.com/term-llm/term-llm/internal/tools"
)

func truncateShellDescription(desc string) string {
	runes := []rune(desc)
	if len(runes) > 100 {
		return string(runes[:97]) + "..."
	}
	return desc
}

func buildExpandedShellInfo(args tools.ShellArgs) string {
	if args.Command == "" {
		return ""
	}
	var b strings.Builder
	if args.Description != "" {
		desc := truncateShellDescription(terminaltext.SanitizeSingleLine(args.Description))
		b.WriteString(desc)
		b.WriteString("\n")
	}
	b.WriteString(terminaltext.EscapeControls(args.Command))
	if len(args.Env) > 0 {
		keys := make([]string, 0, len(args.Env))
		for key := range args.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b.WriteString("\n  ")
			b.WriteString(terminaltext.EscapeControls(key))
			b.WriteString("=")
			b.WriteString(terminaltext.EscapeControls(args.Env[key]))
		}
	}
	return b.String()
}
