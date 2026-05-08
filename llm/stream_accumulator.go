package llm

import (
	"encoding/json"
	"strings"
)

// StreamAccumulator collects StreamEvent values and produces a complete Response.
// It primarily exists to bridge streaming mode back to code that expects a Response.
type StreamAccumulator struct {
	textByID      map[string]*strings.Builder
	textOrder     []string
	reasoning     strings.Builder
	toolCalls     map[string]*ToolCallData
	toolCallOrder []string
	finish        *FinishReason
	usage         *Usage
	final         *Response
	partial       *Response
}

func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{
		textByID:  map[string]*strings.Builder{},
		toolCalls: map[string]*ToolCallData{},
	}
}

func (a *StreamAccumulator) Process(ev StreamEvent) {
	if a == nil {
		return
	}
	switch ev.Type {
	case StreamEventTextStart:
		id := strings.TrimSpace(ev.TextID)
		if id == "" {
			id = "text_0"
		}
		if _, ok := a.textByID[id]; !ok {
			a.textByID[id] = &strings.Builder{}
			a.textOrder = append(a.textOrder, id)
		}
	case StreamEventTextDelta:
		id := strings.TrimSpace(ev.TextID)
		if id == "" {
			id = "text_0"
		}
		b, ok := a.textByID[id]
		if !ok {
			b = &strings.Builder{}
			a.textByID[id] = b
			a.textOrder = append(a.textOrder, id)
		}
		if ev.Delta != "" {
			b.WriteString(ev.Delta)
			a.partial = a.buildResponse()
		}
	case StreamEventReasoningDelta:
		if ev.ReasoningDelta != "" {
			a.reasoning.WriteString(ev.ReasoningDelta)
		}
	case StreamEventToolCallStart:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc := &ToolCallData{
				ID:   ev.ToolCall.ID,
				Name: ev.ToolCall.Name,
				Type: ev.ToolCall.Type,
			}
			a.toolCalls[ev.ToolCall.ID] = tc
			a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
		}
	case StreamEventToolCallDelta:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc, ok := a.toolCalls[ev.ToolCall.ID]
			if !ok {
				tc = &ToolCallData{ID: ev.ToolCall.ID}
				a.toolCalls[ev.ToolCall.ID] = tc
				a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
			}
			if tc.Name == "" && ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}
			if tc.Type == "" && ev.ToolCall.Type != "" {
				tc.Type = ev.ToolCall.Type
			}
			if len(ev.ToolCall.Arguments) > 0 {
				tc.Arguments = append(tc.Arguments, ev.ToolCall.Arguments...)
			}
		}
	case StreamEventToolCallEnd:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc, ok := a.toolCalls[ev.ToolCall.ID]
			if !ok {
				tc = &ToolCallData{ID: ev.ToolCall.ID}
				a.toolCalls[ev.ToolCall.ID] = tc
				a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
			}
			if tc.Name == "" && ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}
			if tc.Type == "" && ev.ToolCall.Type != "" {
				tc.Type = ev.ToolCall.Type
			}
			if len(ev.ToolCall.Arguments) > 0 {
				tc.Arguments = append(tc.Arguments[:0], ev.ToolCall.Arguments...)
			}
		}
	case StreamEventFinish:
		a.finish = ev.FinishReason
		a.usage = ev.Usage
		if ev.Response != nil {
			cp := *ev.Response
			a.final = &cp
			a.partial = &cp
			return
		}
		r := a.buildResponse()
		a.final = r
		a.partial = r
	default:
		// ignore
	}
}

// Response returns the final accumulated response after FINISH, or nil if the stream
// has not completed.
func (a *StreamAccumulator) Response() *Response {
	if a == nil {
		return nil
	}
	return a.final
}

// PartialResponse returns the best-effort accumulated response so far (may be nil).
func (a *StreamAccumulator) PartialResponse() *Response {
	if a == nil {
		return nil
	}
	if a.partial != nil {
		cp := *a.partial
		return &cp
	}
	return nil
}

func (a *StreamAccumulator) buildResponse() *Response {
	if a == nil {
		return nil
	}
	var parts []ContentPart

	// Thinking content comes first (matches provider ordering).
	if rt := a.reasoning.String(); rt != "" {
		parts = append(parts, ContentPart{
			Kind:     ContentThinking,
			Thinking: &ThinkingData{Text: rt},
		})
	}

	// Text content.
	var b strings.Builder
	for _, id := range a.textOrder {
		if tb := a.textByID[id]; tb != nil {
			b.WriteString(tb.String())
		}
	}
	if text := b.String(); text != "" {
		parts = append(parts, ContentPart{Kind: ContentText, Text: text})
	}

	// Tool call content.
	for _, id := range a.toolCallOrder {
		tc := a.toolCalls[id]
		if tc != nil {
			cp := *tc
			// Normalize arguments: ensure valid JSON raw message.
			if len(cp.Arguments) > 0 {
				cp.Arguments = json.RawMessage(cp.Arguments)
			}
			parts = append(parts, ContentPart{
				Kind:     ContentToolCall,
				ToolCall: &cp,
			})
		}
	}

	// Ensure at least one content part (empty text) for backward compatibility.
	if len(parts) == 0 {
		parts = append(parts, ContentPart{Kind: ContentText, Text: ""})
	}

	msg := Message{Role: RoleAssistant, Content: parts}
	r := &Response{Message: msg}
	if a.finish != nil {
		r.Finish = *a.finish
	}
	if a.usage != nil {
		r.Usage = *a.usage
	}
	return r
}
