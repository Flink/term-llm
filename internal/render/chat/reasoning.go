package chat

import (
	"github.com/term-llm/term-llm/internal/config"
	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/session"
)

// RenderReasoningPart renders one displayable reasoning part using cfg. It is
// shared by live tail rendering and committed reasoning rerenders.
func RenderReasoningPart(width int, md MarkdownRenderer, toolsExpanded bool, part core.Part, cfg config.ReasoningConfig) string {
	r := NewMessageBlockRenderer(width, md, toolsExpanded)
	r.SetReasoningConfig(cfg)
	block := r.Render(&session.Message{
		Role:  core.RoleAssistant,
		Parts: []core.Part{part},
	})
	if block == nil {
		return ""
	}
	return block.Rendered
}
