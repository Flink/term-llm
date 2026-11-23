package engine

import (
	"strings"
	"unicode/utf8"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
)

// retainTextLocked keeps the final unread text tail without moving text across
// images. Observed output has already left parts and is never replaced.
func (a *codeModeAsyncCell) retainTextLocked(text string) {
	a.parts = append(a.parts, codeModeRetainedPart{
		ToolContentPart: core.ToolContentPart{Type: core.ToolContentPartText, Text: text},
		media:           a.capability.mediaSpans(text),
	})
	a.pendingBytes += len(text)
	excess := a.pendingBytes - codemode.SessionLimits.RetainBytes
	if excess <= 0 {
		return
	}
	a.truncated = true
	keep := a.parts[:0]
	for _, part := range a.parts {
		if excess > 0 && part.Type == core.ToolContentPartText {
			cut := min(excess, len(part.Text))
			for cut < len(part.Text) && !utf8.RuneStart(part.Text[cut]) {
				cut++
			}
			part.cutText(cut) // Omitted text, including any clipped URL, grants nothing.
			part.Text = strings.Clone(part.Text)
			a.pendingBytes -= cut
			a.omittedBytes += cut
			excess -= cut
			if part.Text == "" {
				continue
			}
		}
		keep = append(keep, part)
	}
	// Do not retain removed text/image references in the backing array.
	clear(a.parts[len(keep):])
	a.parts = keep
}

// evictTerminal prefers fully observed, drained receipts; ties remain FIFO.
// The caller holds the session lock, as in lookup and teardown.
func (o *codeModeSessionCells) evictTerminal() {
	index := 0
	for i, cell := range o.terminal {
		cell.mu.Lock()
		drained := cell.observed && len(cell.parts) == 0
		cell.mu.Unlock()
		if drained {
			index = i
			break
		}
	}
	o.terminal[index].dropRetained()
	copy(o.terminal[index:], o.terminal[index+1:])
	o.terminal[len(o.terminal)-1] = nil
	o.terminal = o.terminal[:len(o.terminal)-1]
}

// codeModeRetainedPart keeps local grants alongside their original text spans,
// not in provider content. Offsets stay relative to the original emission.
type codeModeRetainedPart struct {
	core.ToolContentPart
	media  []codeModeMediaSpan
	offset int
}

// textLimit moves a pagination boundary before a matched URL rather than
// rendering an unusable partial URL in two separate tool results.
func (p codeModeRetainedPart) textLimit(limit int) int {
	end := p.offset + limit
	for _, span := range p.media {
		if span.start >= end {
			break
		}
		if span.end > end {
			return span.start - p.offset
		}
	}
	return limit
}

// cutText returns only complete URLs in the removed prefix and invalidates
// any URL the cut crosses (possible when retaining the last unread tail).
func (p *codeModeRetainedPart) cutText(bytes int) []codeModeMediaSpan {
	p.offset += bytes
	p.Text = p.Text[bytes:]
	complete := 0
	for complete < len(p.media) && p.media[complete].end <= p.offset {
		complete++
	}
	removed := p.media[:complete]
	p.media = p.media[complete:]
	if len(p.media) > 0 && p.media[0].start < p.offset {
		p.media = p.media[1:]
	}
	if len(p.media) == 0 {
		p.media = nil
	}
	return removed
}
