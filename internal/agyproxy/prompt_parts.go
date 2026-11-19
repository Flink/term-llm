package agyproxy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	promptPartsMarker = "<term_llm_prompt_parts"
	promptPartsClose  = "\n</term_llm_prompt_parts>"
)

var promptPartsHeader = regexp.MustCompile(`\A<term_llm_prompt_parts sha256="([0-9a-f]{64})">\n`)

// PromptPart is one ordered element of a staged multimodal user turn.
// Exactly one of Text or InlineData must be populated.
type PromptPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *PromptInlineData `json:"inlineData,omitempty"`
}

// PromptInlineData is a Gemini-format inline media payload forwarded to Cloud Code.
type PromptInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type stagedPromptPartsPayload struct {
	Fallback string       `json:"fallback"`
	Parts    []PromptPart `json:"parts"`
}

func promptPartsEnvelope(hash, fallback string) string {
	return `<term_llm_prompt_parts sha256="` + hash + `">` + "\n" + fallback + promptPartsClose
}

// StagePromptParts persists an ordered multimodal user prompt (text and inline
// media) in the private agy home and returns a compact text transport envelope.
// agy stores only the text fallback in its conversation DB, while agyproxy
// restores the full Gemini parts (including inlineData images) on every
// generation and native resume.
func StagePromptParts(home string, parts []PromptPart, fallback string) (string, error) {
	if len(parts) == 0 {
		return "", errors.New("stage agy prompt parts: no parts provided")
	}
	if !utf8.ValidString(fallback) {
		return "", errors.New("stage agy prompt parts: fallback is not valid UTF-8")
	}
	if strings.TrimSpace(fallback) == "" {
		fallback = "[image]"
	}
	if err := validatePromptParts(parts); err != nil {
		return "", fmt.Errorf("stage agy prompt parts: %w", err)
	}
	encoded, err := json.Marshal(stagedPromptPartsPayload{Fallback: fallback, Parts: parts})
	if err != nil {
		return "", fmt.Errorf("stage agy prompt parts: %w", err)
	}
	if len(encoded) > maxPromptSize {
		return "", errors.New("stage agy prompt parts: payload exceeds 128 MiB limit")
	}
	dir, err := openPromptDirectory(home, true)
	if err != nil {
		return "", fmt.Errorf("stage agy prompt parts: %w", err)
	}
	defer dir.Close()
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	name := hash + ".json"
	if _, err := dir.Lstat(name); err == nil {
		if _, err := readStagedPromptParts(dir, hash); err != nil {
			return "", fmt.Errorf("stage agy prompt parts: %w", err)
		}
		return promptPartsEnvelope(hash, fallback), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect staged agy prompt parts: %w", err)
	}
	if err := writeStagedPrompt(dir, name, string(encoded)); err != nil {
		return "", fmt.Errorf("stage agy prompt parts: %w", err)
	}
	if _, err := readStagedPromptParts(dir, hash); err != nil {
		return "", fmt.Errorf("verify staged agy prompt parts: %w", err)
	}
	return promptPartsEnvelope(hash, fallback), nil
}

func validatePromptParts(parts []PromptPart) error {
	for i, part := range parts {
		hasText := part.Text != ""
		hasData := part.InlineData != nil
		if hasText == hasData {
			return fmt.Errorf("part %d must specify exactly one of text or inlineData", i)
		}
		if hasText {
			if !utf8.ValidString(part.Text) {
				return fmt.Errorf("part %d text is not valid UTF-8", i)
			}
			continue
		}
		mime := strings.TrimSpace(part.InlineData.MIMEType)
		data := strings.TrimSpace(part.InlineData.Data)
		if mime == "" || !utf8.ValidString(mime) {
			return fmt.Errorf("part %d missing inlineData mimeType", i)
		}
		if data == "" {
			return fmt.Errorf("part %d missing inlineData data", i)
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return fmt.Errorf("part %d has invalid base64 inlineData: %w", i, err)
		}
	}
	return nil
}

func readStagedPromptParts(dir *os.Root, hash string) (stagedPromptPartsPayload, error) {
	name := hash + ".json"
	info, err := dir.Lstat(name)
	if err != nil {
		return stagedPromptPartsPayload{}, fmt.Errorf("inspect staged prompt parts: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxPromptSize {
		return stagedPromptPartsPayload{}, errors.New("staged prompt parts must be a private 0600 regular file at most 128 MiB")
	}
	file, err := dir.Open(name)
	if err != nil {
		return stagedPromptPartsPayload{}, fmt.Errorf("open staged prompt parts: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return stagedPromptPartsPayload{}, errors.New("staged prompt parts changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxPromptSize+1))
	if err != nil {
		return stagedPromptPartsPayload{}, fmt.Errorf("read staged prompt parts: %w", err)
	}
	if len(content) > maxPromptSize || fmt.Sprintf("%x", sha256.Sum256(content)) != hash || !utf8.Valid(content) {
		return stagedPromptPartsPayload{}, errors.New("staged prompt parts exceed limit or fail SHA-256/UTF-8 integrity check")
	}
	var payload stagedPromptPartsPayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return stagedPromptPartsPayload{}, fmt.Errorf("decode staged prompt parts: %w", err)
	}
	if len(payload.Parts) == 0 || !utf8.ValidString(payload.Fallback) {
		return stagedPromptPartsPayload{}, errors.New("staged prompt parts payload is empty or invalid")
	}
	if err := validatePromptParts(payload.Parts); err != nil {
		return stagedPromptPartsPayload{}, err
	}
	return payload, nil
}

func expandStagedPromptParts(text, home string) ([]any, int, bool, error) {
	prefix, body := "", text
	if strings.HasPrefix(body, userRequestOpen) {
		prefix, body = userRequestOpen, body[len(userRequestOpen):]
	}
	if !strings.HasPrefix(body, promptPartsMarker) {
		return nil, 0, false, nil
	}
	match := promptPartsHeader.FindStringSubmatch(body)
	if match == nil {
		return nil, 0, false, errors.New("invalid staged prompt parts marker")
	}
	dir, err := openPromptDirectory(home, false)
	if err != nil {
		return nil, 0, false, err
	}
	defer dir.Close()
	staged, err := readStagedPromptParts(dir, match[1])
	if err != nil {
		return nil, 0, false, err
	}
	wire := promptPartsEnvelope(match[1], staged.Fallback)
	if strings.HasPrefix(body, wire) {
		tail := body[len(wire):]
		if prefix == "" && tail == "" {
			parts, size := buildExpandedGeminiParts("", staged.Parts, "")
			return parts, size, true, nil
		}
		if prefix != "" && validPromptMetadataTail(tail) {
			parts, size := buildExpandedGeminiParts(prefix, staged.Parts, tail)
			return parts, size, true, nil
		}
	}
	cut := promptTruncation.FindStringSubmatchIndex(body)
	if cut == nil {
		return nil, 0, false, errors.New("staged prompt parts payload does not match staged file")
	}
	omitted, err := strconv.ParseUint(body[cut[2]:cut[3]], 10, 64)
	if err != nil || omitted == 0 {
		return nil, 0, false, errors.New("invalid staged prompt parts truncation count")
	}
	expected := wire
	if prefix != "" {
		expected += userRequestClose
	}
	if !promptPrefixMatches(expected, body[:cut[0]]) {
		return nil, 0, false, errors.New("truncated staged prompt parts prefix does not match staged file")
	}
	tail := ""
	if prefix != "" {
		tail = userRequestClose
	}
	parts, size := buildExpandedGeminiParts(prefix, staged.Parts, tail)
	return parts, size, true, nil
}

func buildExpandedGeminiParts(prefix string, staged []PromptPart, tail string) ([]any, int) {
	out := make([]any, 0, len(staged)+2)
	totalBytes := 0
	for i, p := range staged {
		if p.InlineData != nil {
			if i == 0 && prefix != "" {
				out = append(out, map[string]any{"text": prefix})
				totalBytes += len(prefix)
			}
			out = append(out, map[string]any{
				"inlineData": map[string]any{
					"mimeType": strings.TrimSpace(p.InlineData.MIMEType),
					"data":     strings.TrimSpace(p.InlineData.Data),
				},
			})
			totalBytes += len(p.InlineData.MIMEType) + len(p.InlineData.Data)
			if i == len(staged)-1 && tail != "" {
				out = append(out, map[string]any{"text": tail})
				totalBytes += len(tail)
			}
			continue
		}
		text := p.Text
		if i == 0 && prefix != "" {
			text = prefix + text
		}
		if i == len(staged)-1 && tail != "" {
			text += tail
		}
		out = append(out, map[string]any{"text": text})
		totalBytes += len(text)
	}
	return out, totalBytes
}
