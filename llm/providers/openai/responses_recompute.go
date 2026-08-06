package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response from
// a stored API-log response body, for serf-doctor's `apilog --recompute`:
// historical records whose TextLength/ToolCalls were recorded as zero
// because they predate the accumulated-item settlement fix (see
// decodeResponsesStream / settleResponsesTerminalOutput). body is the raw,
// decoded response body exactly as it was received on the wire -- either raw
// Responses-API SSE text (the shape recorded by the streaming Complete/Stream
// path, which is the only Responses-API path this adapter uses) or a single
// JSON object (Chat Completions, non-streamed). requestedModel is used as a
// fallback when the recorded payload omits its own model field, matching
// fromResponses' behavior.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("openai: empty recorded response body")
	}
	if trimmed[0] == '{' {
		var probe struct {
			Choices json.RawMessage `json:"choices"`
		}
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return llm.Response{}, fmt.Errorf("openai: decode recorded response body: %w", err)
		}
		if probe.Choices != nil {
			return extractChatCompletionsFromJSON(trimmed, requestedModel)
		}
		return extractResponsesFromJSON(trimmed, requestedModel)
	}
	if isChatCompletionsSSE(trimmed) {
		return extractChatCompletionsFromSSE(trimmed, requestedModel)
	}
	return extractResponsesFromSSE(trimmed, requestedModel)
}

// isChatCompletionsSSE peeks at the first SSE data payload to tell a Chat
// Completions chunk stream (each chunk carries "choices", ending in a literal
// "data: [DONE]") apart from a Responses-API event stream (each event
// carries a "type" field like "response.output_item.done" and no
// top-level "choices").
func isChatCompletionsSSE(body []byte) bool {
	found := false
	stop := errors.New("stop")
	_ = llm.ParseSSE(context.Background(), bytes.NewReader(body), func(ev llm.SSEEvent) error {
		data := strings.TrimSpace(string(ev.Data))
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			found = true
			return stop
		}
		var probe struct {
			Choices json.RawMessage `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &probe); err == nil && probe.Choices != nil {
			found = true
		}
		return stop
	})
	return found
}

// extractResponsesFromJSON handles a non-streamed Responses-API response
// body (Adapter.Complete's JSON path): the recorded payload already is the
// single settled response object, so no accumulation is needed.
func extractResponsesFromJSON(body []byte, requestedModel string) (llm.Response, error) {
	raw, err := decodeJSONObject(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai: decode recorded responses body: %w", err)
	}
	return fromResponses(raw, requestedModel), nil
}

// extractResponsesFromSSE offline-replays a stored Responses-API SSE body to
// recover the settled Response, reusing the same accumulation shape
// decodeResponsesStream builds live (response.output_item.done events feed
// accumulatedOutput) and the same terminal-settlement rule
// (settleResponsesTerminalOutput) that decides whether the terminal payload
// or the accumulated items are authoritative.
func extractResponsesFromSSE(body []byte, requestedModel string) (llm.Response, error) {
	type toolState struct {
		id, itemID, name string
		args             strings.Builder
	}
	toolStates := map[string]*toolState{}
	itemToCallID := map[string]string{}
	var accumulatedOutput []any
	var terminal map[string]any

	parseErr := llm.ParseSSE(context.Background(), bytes.NewReader(body), func(ev llm.SSEEvent) error {
		if len(ev.Data) == 0 {
			return nil
		}
		payload, ok := decodeSSEPayload(ev.Data)
		if !ok {
			return nil
		}
		typ, _ := payload["type"].(string)
		if typ == "" {
			typ = ev.Event
		}
		switch typ {
		case "response.function_call_arguments.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				delta, _ = payload["arguments"].(string)
			}
			callID, _ := payload["call_id"].(string)
			itemID, _ := payload["item_id"].(string)
			if callID == "" && itemID != "" {
				callID = itemToCallID[itemID]
			}
			if callID == "" {
				callID = itemID
			}
			if callID == "" || delta == "" {
				return nil
			}
			st := toolStates[callID]
			if st == nil {
				st = &toolState{id: callID}
				toolStates[callID] = st
			}
			st.args.WriteString(delta)
		case "response.output_item.done":
			itemAny := payload["item"]
			if itemAny == nil {
				itemAny = payload["output_item"]
			}
			item, ok := itemAny.(map[string]any)
			if !ok {
				return nil
			}
			it, _ := item["type"].(string)
			if it != "function_call" {
				accumulatedOutput = append(accumulatedOutput, item)
				return nil
			}
			callID, _ := item["call_id"].(string)
			itemID, _ := item["id"].(string)
			name, _ := item["name"].(string)
			argsStr, _ := item["arguments"].(string)
			if callID == "" && itemID != "" {
				callID = itemToCallID[itemID]
			}
			if callID == "" {
				callID = itemID
			}
			if callID != "" {
				itemToCallID[itemID] = callID
				if argsStr == "" {
					if st := toolStates[callID]; st != nil {
						argsStr = st.args.String()
					}
				}
			}
			accumulatedOutput = append(accumulatedOutput, map[string]any{
				"type":      "function_call",
				"call_id":   callID,
				"id":        itemID,
				"name":      name,
				"arguments": argsStr,
			})
		case "response.completed":
			rawResp, _ := payload["response"].(map[string]any)
			if rawResp == nil {
				rawResp = payload
			}
			terminal = rawResp
		}
		return nil
	})
	if parseErr != nil {
		return llm.Response{}, fmt.Errorf("openai: parse recorded responses SSE body: %w", parseErr)
	}
	if terminal == nil {
		return llm.Response{}, errors.New("openai: recorded responses SSE body has no response.completed event")
	}

	r := fromResponses(terminal, requestedModel)
	settleResponsesTerminalOutput(&r, terminal, accumulatedOutput)
	return r, nil
}

// extractChatCompletionsFromJSON handles a non-streamed Chat Completions
// response body: a single choice carrying the complete message, so (unlike
// the SSE chunk stream) no accumulation is needed.
func extractChatCompletionsFromJSON(body []byte, requestedModel string) (llm.Response, error) {
	var wire struct {
		Model   string         `json:"model"`
		Choices []chatChoice   `json:"choices"`
		Usage   map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return llm.Response{}, fmt.Errorf("openai: decode recorded chat completions body: %w", err)
	}
	model := wire.Model
	if model == "" {
		model = requestedModel
	}
	r := llm.Response{Provider: "openai", Model: model}
	if wire.Usage != nil {
		r.Usage = openaichat.ParseChatUsage(wire.Usage)
	}
	if len(wire.Choices) > 0 {
		choice := wire.Choices[0]
		r.Message = choice.message()
		r.Finish = llm.NormalizeFinishReason("", choice.FinishReason)
	}
	return r, nil
}

// chatChoice is the non-streamed Chat Completions choice shape: a complete
// message rather than the SSE chunk stream's incremental delta.
type chatChoice struct {
	Message struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

func (c chatChoice) message() llm.Message {
	msg := llm.Message{Role: llm.RoleAssistant}
	if c.Message.Content != "" {
		msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: c.Message.Content})
	}
	for _, tc := range c.Message.ToolCalls {
		msg.Content = append(msg.Content, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
				Type:      "function",
			},
		})
	}
	return msg
}

// extractChatCompletionsFromSSE offline-replays a stored Chat Completions
// SSE chunk stream, accumulating text/tool-call deltas the same way
// decodeChatCompletionsStream does live, up to its final "[DONE]" handling.
func extractChatCompletionsFromSSE(body []byte, requestedModel string) (llm.Response, error) {
	type toolCallState struct {
		id   string
		name string
		args strings.Builder
	}
	toolCallOrder := []int{}
	toolCalls := map[int]*toolCallState{}
	var textBuf strings.Builder
	var finishReason, model string
	var usage *llm.Usage
	done := false

	parseErr := llm.ParseSSE(context.Background(), bytes.NewReader(body), func(ev llm.SSEEvent) error {
		data := strings.TrimSpace(string(ev.Data))
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id,omitempty"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil //nolint:nilerr // unparseable chunk is skipped, matching decodeChatCompletionsStream
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			u := openaichat.ParseChatUsage(chunk.Usage)
			usage = &u
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		textBuf.WriteString(choice.Delta.Content)
		for _, tc := range choice.Delta.ToolCalls {
			state, exists := toolCalls[tc.Index]
			if !exists {
				state = &toolCallState{id: tc.ID, name: tc.Function.Name}
				toolCalls[tc.Index] = state
				toolCallOrder = append(toolCallOrder, tc.Index)
			}
			state.args.WriteString(tc.Function.Arguments)
		}
		return nil
	})
	if parseErr != nil {
		return llm.Response{}, fmt.Errorf("openai: parse recorded chat completions SSE body: %w", parseErr)
	}
	if !done {
		return llm.Response{}, errors.New("openai: recorded chat completions SSE body has no [DONE] event")
	}
	if model == "" {
		model = requestedModel
	}

	sort.Ints(toolCallOrder)
	msg := llm.Message{Role: llm.RoleAssistant}
	if textBuf.Len() > 0 {
		msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
	}
	for _, idx := range toolCallOrder {
		tc := toolCalls[idx]
		msg.Content = append(msg.Content, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        tc.id,
				Name:      tc.name,
				Arguments: json.RawMessage(tc.args.String()),
				Type:      "function",
			},
		})
	}

	r := llm.Response{Provider: "openai", Model: model, Message: msg, Finish: llm.NormalizeFinishReason("", finishReason)}
	if usage != nil {
		r.Usage = *usage
	}
	return r, nil
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeSSEPayload(data []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil, false
	}
	return payload, true
}
