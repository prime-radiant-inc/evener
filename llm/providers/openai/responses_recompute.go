package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response
// from a stored API-log response body recorded for the Responses API
// (endpoint families openai_public/openai_codex -- see
// Adapter.responsesEndpointFamily; openaicompat's codex-continuation family
// delegates to this same adapter, so its records carry these same values
// too), for serf-doctor's `apilog --recompute`: historical records whose
// TextLength/ToolCalls were recorded as zero because they predate the
// accumulated-item settlement fix (see decodeResponsesStream /
// settleResponsesTerminalOutput). body is the raw, decoded response body
// exactly as it was received on the wire -- either raw Responses-API SSE
// text (the shape recorded by the streaming Complete/Stream path) or a
// single JSON object (Adapter.Complete's non-streamed path). requestedModel
// is used as a fallback when the recorded payload omits its own model
// field, matching fromResponses' behavior.
//
// For the Chat Completions endpoint families, see
// ExtractRecordedChatCompletionsResponse (openai_chat_completions, this
// package) and openaicompat.ExtractRecordedResponse
// (openai_compatible_chat_completions) -- each reuses that family's own
// live parser rather than a shape-sniffed guess, since the two families'
// live decoders are genuinely different implementations.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("openai: empty recorded response body")
	}
	if trimmed[0] == '{' {
		raw, err := decodeJSONObject(trimmed)
		if err != nil {
			return llm.Response{}, fmt.Errorf("openai: decode recorded responses body: %w", err)
		}
		if _, hasChoices := raw["choices"]; hasChoices {
			return llm.Response{}, errors.New("openai: recorded body is a Chat Completions shape, not Responses API -- use ExtractRecordedChatCompletionsResponse or openaicompat.ExtractRecordedResponse")
		}
		return fromResponses(raw, requestedModel), nil
	}
	if isChatCompletionsSSE(trimmed) {
		return llm.Response{}, errors.New("openai: recorded body is a Chat Completions SSE stream, not Responses API -- use ExtractRecordedChatCompletionsResponse or openaicompat.ExtractRecordedResponse")
	}
	return extractResponsesFromSSE(trimmed, requestedModel)
}

// ExtractRecordedChatCompletionsResponse offline re-extracts the canonical
// llm.Response from a stored API-log response body recorded for this
// adapter's own Chat Completions fallback (endpoint family
// openai_chat_completions -- see streamViaChatCompletions). That family is
// always streamed in this codebase (there is no non-streamed call path for
// it), so body must be a Chat Completions SSE chunk stream; a JSON body
// under this family has no live parser to reuse and is rejected rather than
// guessed at.
func ExtractRecordedChatCompletionsResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("openai: empty recorded response body")
	}
	if trimmed[0] == '{' {
		return llm.Response{}, errors.New("openai: recorded openai_chat_completions body is JSON, but this endpoint family has no non-streamed live parser to reuse (it is always streamed)")
	}
	return extractChatCompletionsFromSSE(trimmed, requestedModel)
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

// extractResponsesFromSSE offline-replays a stored Responses-API SSE body to
// recover the settled Response, driving the same responsesOutputAccumulator
// decodeResponsesStream drives live (response.output_item.added/done and
// response.function_call_arguments.delta/done events feed the identical
// accumulation state machine) and the same terminal-settlement rule
// (settleResponsesTerminalOutput) that decides whether the terminal payload
// or the accumulated items are authoritative.
func extractResponsesFromSSE(body []byte, requestedModel string) (llm.Response, error) {
	acc := newResponsesOutputAccumulator()
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
		case "response.output_item.added":
			if item, ok := payload["item"].(map[string]any); ok {
				acc.HandleOutputItemAdded(item)
			}
		case "response.function_call_arguments.delta":
			acc.HandleFunctionCallArgumentsDelta(payload)
		case "response.function_call_arguments.done":
			acc.HandleFunctionCallArgumentsDone(payload)
		case "response.output_item.done":
			acc.HandleOutputItemDone(payload)
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
	settleResponsesTerminalOutput(&r, terminal, acc.Output())
	return r, nil
}

// extractChatCompletionsFromSSE offline-replays a stored Chat Completions
// SSE chunk stream, driving the same chatCompletionsChunkAccumulator
// decodeChatCompletionsStream drives live, up to its final "[DONE]"
// settlement.
func extractChatCompletionsFromSSE(body []byte, requestedModel string) (llm.Response, error) {
	acc := newChatCompletionsChunkAccumulator()
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
		var chunk chatCompletionsChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil //nolint:nilerr // unparseable chunk is skipped, matching decodeChatCompletionsStream
		}
		acc.HandleChunkMeta(chunk.Model, chunk.Usage)
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		acc.HandleFinishReason(choice.FinishReason)
		if choice.Delta.Content != "" {
			acc.HandleContentDelta(choice.Delta.Content)
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc.HandleToolCallDelta(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		return nil
	})
	if parseErr != nil {
		return llm.Response{}, fmt.Errorf("openai: parse recorded chat completions SSE body: %w", parseErr)
	}
	if !done {
		return llm.Response{}, errors.New("openai: recorded chat completions SSE body has no [DONE] event")
	}

	r := acc.Settle()
	if r.Model == "" {
		r.Model = requestedModel
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
