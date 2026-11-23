package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/term-llm/term-llm/internal/codemode"
	"github.com/term-llm/term-llm/internal/llm/core"
	_ "golang.org/x/image/webp"
)

func (c *nestedCell) project(output core.ToolOutput) (json.RawMessage, error) {
	if output.ProgrammaticOversized {
		return nil, fmt.Errorf("raw MCP result exceeds programmatic limit; dispatched outcome may have effects")
	}
	var object map[string]json.RawMessage
	if len(output.Programmatic) > 0 {
		if err := json.Unmarshal(output.Programmatic, &object); err != nil {
			return nil, fmt.Errorf("invalid programmatic JSON: %w", err)
		}
	} else {
		b, err := json.Marshal(map[string]any{"text": output.Content, "isError": output.IsError, "timedOut": output.TimedOut})
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(b, &object)
		blocks := []map[string]any{}
		for _, p := range output.ContentParts {
			if p.Type == core.ToolContentPartImageData && p.ImageData != nil {
				blocks = append(blocks, map[string]any{"type": "image", "data": p.ImageData.Base64, "mimeType": p.ImageData.MediaType})
			}
		}
		if len(blocks) > 0 {
			object["content"], _ = json.Marshal(blocks)
		}
	}
	if object == nil {
		return nil, fmt.Errorf("programmatic envelope must be an object")
	}
	if content, ok := object["content"]; ok {
		var blocks []map[string]json.RawMessage
		if err := json.Unmarshal(content, &blocks); err != nil {
			return nil, fmt.Errorf("invalid content blocks: %w", err)
		}
		for _, block := range blocks {
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			if kind != "image" {
				continue
			}
			var data, mime string
			if json.Unmarshal(block["data"], &data) != nil || json.Unmarshal(block["mimeType"], &mime) != nil {
				return nil, fmt.Errorf("invalid image block")
			}
			handle, err := c.saveImage(data, mime)
			if err != nil {
				return nil, err
			}
			delete(block, "data")
			block["handle"], _ = json.Marshal(handle)
		}
		object["content"], _ = json.Marshal(blocks)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if len(raw) > codemode.Limits.RawResult {
		return nil, fmt.Errorf("programmatic result exceeds %d bytes; action completed", codemode.Limits.RawResult)
	}
	return raw, nil
}

func validateCellImage(data, mime string) (core.ToolImageData, int, error) {
	if len(data) > base64.StdEncoding.EncodedLen(codemode.Limits.ImageBytes) {
		return core.ToolImageData{}, 0, fmt.Errorf("image byte limit")
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
		return core.ToolImageData{}, 0, fmt.Errorf("unsupported image MIME type")
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return core.ToolImageData{}, 0, fmt.Errorf("invalid base64 image: %w", err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return core.ToolImageData{}, 0, fmt.Errorf("image decode/pixel limit")
	}
	if "image/"+format != mime && !(format == "jpeg" && mime == "image/jpeg") {
		return core.ToolImageData{}, 0, fmt.Errorf("image MIME mismatch")
	}
	if _, _, err = image.Decode(bytes.NewReader(raw)); err != nil {
		return core.ToolImageData{}, 0, fmt.Errorf("invalid image: %w", err)
	}
	return core.ToolImageData{MediaType: mime, Base64: data, Width: config.Width, Height: config.Height}, len(raw), nil
}
func (c *nestedCell) saveImage(data, mime string) (string, error) {
	img, n, err := validateCellImage(data, mime)
	if err != nil {
		return "", err
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return "", err
	}
	handle := "cell-image:" + hex.EncodeToString(token)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", fmt.Errorf("cell closed")
	}
	if len(c.images) >= codemode.Limits.Calls || c.imageBytes+n > codemode.Limits.ImageBytes {
		return "", fmt.Errorf("image artifact limit")
	}
	c.imageBytes += n
	c.images[handle] = img
	return handle, nil
}
func (c *nestedCell) Image(value json.RawMessage) (core.ToolContentPart, error) {
	var handle string
	if json.Unmarshal(value, &handle) != nil {
		var block struct {
			Type     string `json:"type"`
			Handle   string `json:"handle"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(value, &block); err != nil || block.Type != "image" {
			return core.ToolContentPart{}, fmt.Errorf("image expects an MCP image block or current-cell host handle")
		}
		handle = block.Handle
		if handle == "" {
			var err error
			handle, err = c.saveImage(block.Data, block.MimeType)
			if err != nil {
				return core.ToolContentPart{}, err
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.images[handle]
	if !ok || c.closed {
		return core.ToolContentPart{}, fmt.Errorf("forged, expired or cross-cell image handle")
	}
	return core.ToolContentPart{Type: core.ToolContentPartImageData, ImageData: &img}, nil
}

// materializeCellImages uses the ordinary approved inline-image cache only
// after explicit image emission; JS never obtains filesystem-path authority.
func materializeCellImages(output core.ToolOutput) core.ToolOutput {
	for _, part := range output.ContentParts {
		if part.Type == core.ToolContentPartImageData && part.ImageData != nil {
			if path, ok := saveImageDataToUploads(part.ImageData); ok {
				output.Images = append(output.Images, path)
			}
		}
	}
	return output
}

// rememberMedia retains only host metadata from successful native text
// results, never from raw programmatic data, arguments or image emissions.
func (c *nestedCell) rememberMedia(output core.ToolOutput) {
	if len(output.Media) == 0 || output.IsError || output.TimedOut || len(output.Programmatic) != 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || len(c.media) >= codemode.Limits.Calls {
		return
	}
	for _, url := range core.CodeModeMediaURLs(output.Content) {
		for _, artifact := range output.Media {
			if artifact.Reference != strings.TrimPrefix(url, core.CodeModeMediaURLPrefix) || artifact.Path() == "" {
				continue
			}
			if c.media == nil {
				c.media = make(map[string]*core.MediaArtifact)
			}
			if _, exists := c.media[url]; !exists {
				c.media[strings.Clone(url)] = &artifact
			}
			break
		}
		if len(c.media) >= codemode.Limits.Calls {
			return
		}
	}
}

// EmittedMedia is used by the legacy tool before adding its status receipt.
func (c *nestedCell) EmittedMedia(parts []core.ToolContentPart) []core.MediaArtifact {
	var media codeModeObservedMedia
	for _, part := range parts {
		if part.Type == core.ToolContentPartText {
			media.add(c.mediaSpans(part.Text))
		}
	}
	return media.artifacts
}

// Match before retention or pagination, while the original URL boundaries
// and the grants at the time of emission are still available. Queued spans
// outlive capability closure; unreferenced artifacts do not.
func (c *nestedCell) mediaSpans(text string) []codeModeMediaSpan {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.media) == 0 {
		return nil
	}
	var spans []codeModeMediaSpan
	for start, url := range core.CodeModeMediaURLs(text) {
		if artifact := c.media[url]; artifact != nil {
			spans = append(spans, codeModeMediaSpan{start: start, end: start + len(url), artifact: artifact})
		}
	}
	return spans
}

type codeModeMediaSpan struct {
	start, end int
	artifact   *core.MediaArtifact
}

type codeModeObservedMedia struct {
	seen      map[string]bool
	artifacts []core.MediaArtifact
}

func (m *codeModeObservedMedia) add(spans []codeModeMediaSpan) {
	for _, span := range spans {
		ref := span.artifact.Reference
		if !m.seen[ref] {
			if m.seen == nil {
				m.seen = make(map[string]bool)
			}
			m.seen[ref] = true
			m.artifacts = append(m.artifacts, *span.artifact)
		}
	}
}
