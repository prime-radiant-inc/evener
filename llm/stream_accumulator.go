package llm

import (
	"slices"
	"strings"
	"sync"
)

// StreamAccumulator collects StreamEvent values and produces a complete Response.
// It primarily exists to bridge streaming mode back to code that expects a Response.
//
// A StreamAccumulator is safe for concurrent use: Process may run on a producer
// goroutine while Response or PartialResponse projects the accumulated state on a
// consumer goroutine.
type StreamAccumulator struct {
	mu            sync.Mutex
	textByID      map[string]*strings.Builder
	textOrder     []string
	reasoning     strings.Builder
	toolCalls     map[string]*ToolCallData
	toolCallOrder []string
	finish        *FinishReason
	usage         *Usage
	final         *Response
	partial       *Response
	dirty         bool // true when partial is stale and must be rebuilt before the next read
	buildCount    int  // number of buildResponse() calls; test-visible proof of lazy rebuild
}

// NewStreamAccumulator returns a StreamAccumulator with its internal text and
// tool-call maps initialized and ready to Process events.
func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{
		textByID:  map[string]*strings.Builder{},
		toolCalls: map[string]*ToolCallData{},
	}
}

// Process folds a single StreamEvent into the accumulator, updating accumulated
// text, reasoning, tool calls, finish reason, and usage according to the event's
// type. It is a no-op on a nil receiver and ignores unrecognized event types.
func (a *StreamAccumulator) Process(ev StreamEvent) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
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
			a.dirty = true
		}
	case StreamEventReasoningDelta:
		if ev.ReasoningDelta != "" {
			a.reasoning.WriteString(ev.ReasoningDelta)
			a.dirty = true
		}
	case StreamEventToolCallStart:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc := &ToolCallData{
				ID:               ev.ToolCall.ID,
				ItemID:           ev.ToolCall.ItemID,
				Name:             ev.ToolCall.Name,
				Type:             ev.ToolCall.Type,
				ThoughtSignature: ev.ToolCall.ThoughtSignature,
			}
			a.toolCalls[ev.ToolCall.ID] = tc
			a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
			a.dirty = true
		}
	case StreamEventToolCallDelta:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc, ok := a.toolCalls[ev.ToolCall.ID]
			if !ok {
				tc = &ToolCallData{
					ID:               ev.ToolCall.ID,
					ItemID:           ev.ToolCall.ItemID,
					ThoughtSignature: ev.ToolCall.ThoughtSignature,
				}
				a.toolCalls[ev.ToolCall.ID] = tc
				a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
				a.dirty = true
			}
			if tc.Name == "" && ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}
			if tc.ItemID == "" && ev.ToolCall.ItemID != "" {
				tc.ItemID = ev.ToolCall.ItemID
			}
			if tc.Type == "" && ev.ToolCall.Type != "" {
				tc.Type = ev.ToolCall.Type
			}
			if tc.ThoughtSignature == "" && ev.ToolCall.ThoughtSignature != "" {
				tc.ThoughtSignature = ev.ToolCall.ThoughtSignature
				a.dirty = true
			}
			if len(ev.ToolCall.Arguments) > 0 {
				tc.Arguments = append(tc.Arguments, ev.ToolCall.Arguments...)
				a.dirty = true
			}
		}
	case StreamEventToolCallEnd:
		if ev.ToolCall != nil && ev.ToolCall.ID != "" {
			tc, ok := a.toolCalls[ev.ToolCall.ID]
			if !ok {
				tc = &ToolCallData{
					ID:               ev.ToolCall.ID,
					ItemID:           ev.ToolCall.ItemID,
					ThoughtSignature: ev.ToolCall.ThoughtSignature,
				}
				a.toolCalls[ev.ToolCall.ID] = tc
				a.toolCallOrder = append(a.toolCallOrder, ev.ToolCall.ID)
				a.dirty = true
			}
			if tc.Name == "" && ev.ToolCall.Name != "" {
				tc.Name = ev.ToolCall.Name
			}
			if tc.ItemID == "" && ev.ToolCall.ItemID != "" {
				tc.ItemID = ev.ToolCall.ItemID
			}
			if tc.Type == "" && ev.ToolCall.Type != "" {
				tc.Type = ev.ToolCall.Type
			}
			if tc.ThoughtSignature == "" && ev.ToolCall.ThoughtSignature != "" {
				tc.ThoughtSignature = ev.ToolCall.ThoughtSignature
				a.dirty = true
			}
			if len(ev.ToolCall.Arguments) > 0 {
				// Clone rather than reuse the accumulated buffer: buildResponse copies a
				// ToolCallData by value, so a partial projected before this event still
				// aliases that array. Rewriting it in place would change bytes a caller
				// already holds. The delta arm stays append-only, which never writes
				// below the length a reader can see.
				tc.Arguments = slices.Clone(ev.ToolCall.Arguments)
				a.dirty = true
			}
		}
	case StreamEventFinish:
		a.finish = ev.FinishReason
		a.usage = ev.Usage
		if ev.Response != nil {
			cp := *ev.Response
			if !responseHasContent(cp) {
				cp = *a.buildResponseLocked()
				copyResponseMetadata(&cp, ev.Response)
			}
			a.final = &cp
			a.partial = &cp
			a.dirty = false
			return
		}
		r := a.buildResponseLocked()
		a.final = r
		a.partial = r
		a.dirty = false
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
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.final
}

// PartialResponse returns the best-effort accumulated response so far (may be nil).
func (a *StreamAccumulator) PartialResponse() *Response {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dirty {
		a.partial = a.buildResponseLocked()
		a.dirty = false
	}
	if a.partial != nil {
		cp := *a.partial
		return &cp
	}
	return nil
}

// hasText reports whether any nonempty text delta has been accumulated.
func (a *StreamAccumulator) hasText() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range a.textOrder {
		if b := a.textByID[id]; b != nil && b.Len() > 0 {
			return true
		}
	}
	return false
}

// buildResponse returns a fresh response assembled from the accumulated state.
func (a *StreamAccumulator) buildResponse() *Response {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buildResponseLocked()
}

// buildResponseLocked is buildResponse for callers already holding a.mu.
func (a *StreamAccumulator) buildResponseLocked() *Response {
	a.buildCount++
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

func responseHasContent(r Response) bool {
	if len(r.Message.Content) == 0 {
		return false
	}
	if len(r.Message.Content) == 1 {
		part := r.Message.Content[0]
		return part.Kind != ContentText || part.Text != "" || part.Phase != ""
	}
	return true
}

func copyResponseMetadata(dst *Response, src *Response) {
	if src == nil {
		return
	}
	dst.ID = src.ID
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Raw != nil {
		dst.Raw = src.Raw
	}
	if src.Finish.Reason != "" || src.Finish.Raw != "" {
		dst.Finish = src.Finish
	}
	if usageHasValue(src.Usage) {
		dst.Usage = src.Usage
	}
	if len(src.Warnings) > 0 {
		dst.Warnings = append([]Warning(nil), src.Warnings...)
	}
	if src.RateLimit != nil {
		dst.RateLimit = src.RateLimit
	}
}

func usageHasValue(u Usage) bool {
	return u.InputTokens != 0 ||
		u.OutputTokens != 0 ||
		u.TotalTokens != 0 ||
		u.ReasoningTokens != nil ||
		u.ReasoningTokensEstimated != nil ||
		u.CacheReadTokens != nil ||
		u.CacheWriteTokens != nil ||
		u.CacheWrite1hTokens != nil ||
		u.Raw != nil
}
