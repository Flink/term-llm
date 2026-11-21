package responsesproviders

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/llm/providers/responses"
	"github.com/term-llm/term-llm/internal/llm/schema"
)

// chatGPTStrictTools records the tools buildChatGPTTools upgraded to strict
// mode, keyed by wire name.
type chatGPTStrictTools struct {
	// schemas holds the original schemas so optional-field nulls can be
	// dropped from calls.
	schemas map[string]map[string]interface{}
	// fallback holds each tool exactly as it is sent without the upgrade.
	fallback map[string]responses.ResponsesTool
}

// buildChatGPTTools sends every tool whose schema can be made strict with
// strict:true. Without strict mode, gpt-6.1-sol sometimes streams endless
// whitespace into a JSON argument string (seen with Playwright's
// browser_run_code_unsafe); constrained decoding keeps it inside valid JSON.
// Tools whose schemas would change meaning stay non-strict, as do tools whose
// strict schema the backend has already rejected in this process.
func buildChatGPTTools(specs []ToolSpec) ([]any, chatGPTStrictTools) {
	tools := BuildResponsesTools(specs)
	var upgraded chatGPTStrictTools
	if len(tools) != len(specs) {
		return tools, upgraded
	}
	for i, spec := range specs {
		tool, ok := tools[i].(responses.ResponsesTool)
		if !ok || tool.Strict || chatGPTStrictRejections.contains(tool.Name, spec.Schema) {
			continue
		}
		params, ok := schema.StrictToolParameters(spec.Schema)
		if !ok {
			continue
		}
		if upgraded.schemas == nil {
			upgraded.schemas = make(map[string]map[string]interface{})
			upgraded.fallback = make(map[string]responses.ResponsesTool)
		}
		upgraded.schemas[tool.Name] = spec.Schema
		upgraded.fallback[tool.Name] = tool
		tool.Parameters = params
		tool.Strict = true
		tools[i] = tool
	}
	return tools, upgraded
}

// chatGPTStrictRejections remembers strict schemas the backend refused (for
// example a keyword strict mode does not support), so later requests send
// those tools non-strict without paying for another rejected request.
var chatGPTStrictRejections strictRejectionCache

type strictRejectionCache struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func strictRejectionKey(name string, raw map[string]interface{}) string {
	data, err := json.Marshal(raw)
	if err != nil {
		return name
	}
	sum := sha256.Sum256(data)
	return name + "\x00" + string(sum[:])
}

func (c *strictRejectionCache) contains(name string, raw map[string]interface{}) bool {
	c.mu.Lock()
	empty := len(c.keys) == 0
	c.mu.Unlock()
	if empty {
		return false
	}
	key := strictRejectionKey(name, raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.keys[key]
	return ok
}

func (c *strictRejectionCache) add(name string, raw map[string]interface{}) {
	key := strictRejectionKey(name, raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil {
		c.keys = make(map[string]struct{})
	}
	c.keys[key] = struct{}{}
}

// rejectionApplies reports whether a schema rejection naming name can be
// caused by the strict upgrade; an unnamed rejection might be.
func (u chatGPTStrictTools) rejectionApplies(name string) bool {
	if name == "" {
		return true
	}
	_, ok := u.schemas[name]
	return ok
}

// recordRejection remembers the rejected tool, or every upgraded tool when the
// backend did not say which one it refused.
func (u chatGPTStrictTools) recordRejection(name string) {
	if raw, ok := u.schemas[name]; ok {
		chatGPTStrictRejections.add(name, raw)
		return
	}
	for name, raw := range u.schemas {
		chatGPTStrictRejections.add(name, raw)
	}
}

// withoutStrict returns req with every upgraded tool restored to its
// non-strict form. Tool names and order are unchanged, so tool_choice and
// replay guards computed for req remain valid.
func (u chatGPTStrictTools) withoutStrict(req ResponsesRequest) ResponsesRequest {
	tools := make([]any, len(req.Tools))
	for i, tool := range req.Tools {
		if strict, ok := tool.(responses.ResponsesTool); ok && strict.Strict {
			if original, ok := u.fallback[strict.Name]; ok {
				tool = original
			}
		}
		tools[i] = tool
	}
	req.Tools = tools
	return req
}

// streamChatGPTWithStrictTools opens the request and, if the backend rejects
// an upgraded strict schema before producing output, resends it once with the
// upgraded tools non-strict. Once that resend produces output the rejected
// tool is remembered and sent non-strict from then on; if the resend fails
// too, strict mode was not the cause and nothing is remembered.
func streamChatGPTWithStrictTools(ctx context.Context, client *ResponsesClient, req ResponsesRequest, debugRaw bool, upgraded chatGPTStrictTools) (Stream, error) {
	stream, err := client.Stream(ctx, req, debugRaw)
	if len(upgraded.schemas) == 0 {
		return stream, err
	}
	s := &strictToolStream{
		current:     stream,
		upgraded:    upgraded,
		nullSchemas: upgraded.schemas,
		retry: func() (Stream, error) {
			return client.Stream(ctx, upgraded.withoutStrict(req), debugRaw)
		},
	}
	if err != nil {
		name, rejected := responses.InvalidFunctionSchemaTool(err)
		if !rejected || !upgraded.rejectionApplies(name) {
			return nil, err
		}
		if err := s.resend(name); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// strictToolStream drops the nulls strict mode makes the model send for
// optional fields, so tools see those fields as omitted rather than null, and
// performs the one non-strict resend when a strict schema is rejected.
type strictToolStream struct {
	current  Stream
	upgraded chatGPTStrictTools
	// nullSchemas selects calls whose optional nulls are dropped; nil once
	// every tool has been resent non-strict.
	nullSchemas map[string]map[string]interface{}
	retry       func() (Stream, error)
	emitted     bool
	// rejected names the tool to remember once the non-strict resend
	// produces output.
	rejected *string
}

func (s *strictToolStream) Recv() (Event, error) {
	event, err := s.current.Recv()
	if s.retry != nil && !s.emitted {
		if cause := strictStreamFailure(event, err); cause != nil {
			if name, rejected := responses.InvalidFunctionSchemaTool(cause); rejected && s.upgraded.rejectionApplies(name) {
				if err := s.resend(name); err != nil {
					return Event{}, err
				}
				return s.Recv()
			}
		}
	}
	if err != nil {
		return event, err
	}
	if event.Type != core.EventHeartbeat && event.Type != core.EventRetry && event.Type != core.EventError {
		s.emitted = true
		if s.rejected != nil {
			s.upgraded.recordRejection(*s.rejected)
			s.rejected = nil
		}
	}
	if event.Type != EventToolCall || event.Tool == nil {
		return event, nil
	}
	if raw, ok := s.nullSchemas[event.Tool.Name]; ok {
		call := *event.Tool
		call.Arguments = schema.DropStrictNulls(raw, call.Arguments)
		event.Tool = &call
	}
	return event, nil
}

func (s *strictToolStream) resend(name string) error {
	retry := s.retry
	s.retry = nil
	if s.current != nil {
		_ = s.current.Close()
	}
	next, err := retry()
	if err != nil {
		return err
	}
	s.current = next
	s.rejected = &name
	// Every tool is non-strict now, so its nulls are the model's own answer.
	s.nullSchemas = nil
	return nil
}

func strictStreamFailure(event Event, err error) error {
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if event.Type == core.EventError {
		return event.Err
	}
	return nil
}

func (s *strictToolStream) Close() error {
	return s.current.Close()
}
