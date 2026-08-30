package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/llm"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response
// from a stored API-log response body recorded for the Responses API
// (endpoint families openai_public and openai_codex), for evener-doctor's
// `apilog --recompute`: historical records whose TextLength/ToolCalls were
// recorded as zero because they predate the accumulated-item settlement fix
// (see decodeStream / settleResponsesTerminalOutput). body is the raw,
// decoded response body exactly as it was received on the wire — either raw
// Responses-API SSE text (the shape the streaming path records) or a single
// JSON object (Complete's non-streamed path). requestedModel is used as a
// fallback when the recorded payload omits its own model field, matching
// fromResponses' behavior.
//
// A Chat Completions body under a Responses family is a mis-filed record,
// not something to guess at: it is rejected here and belongs to
// chatcompletions.ExtractRecordedResponse, which replays it through that
// family's own live decoder.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("responses: empty recorded response body")
	}
	if trimmed[0] == '{' {
		raw, err := decodeJSONObject(trimmed)
		if err != nil {
			return llm.Response{}, fmt.Errorf("responses: decode recorded responses body: %w", err)
		}
		if _, hasChoices := raw["choices"]; hasChoices {
			return llm.Response{}, errors.New("responses: recorded body is a Chat Completions shape, not Responses API -- use chatcompletions.ExtractRecordedResponse")
		}
		return fromResponses(raw, requestedModel), nil
	}
	if isChatCompletionsSSE(trimmed) {
		return llm.Response{}, errors.New("responses: recorded body is a Chat Completions SSE stream, not Responses API -- use chatcompletions.ExtractRecordedResponse")
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

// extractResponsesFromSSE offline-replays a stored Responses-API SSE body to
// recover the settled Response, driving the same responsesOutputAccumulator
// decodeStream drives live (response.output_item.added/done and
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
		return llm.Response{}, fmt.Errorf("responses: parse recorded responses SSE body: %w", parseErr)
	}
	if terminal == nil {
		return llm.Response{}, errors.New("responses: recorded responses SSE body has no response.completed event")
	}

	r := fromResponses(terminal, requestedModel)
	settleResponsesTerminalOutput(&r, terminal, acc.Output())
	return r, nil
}

// decodeJSONObject decodes a response body as a JSON object, keeping numbers
// as json.Number the way every live decode in this package does.
func decodeJSONObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// decodeSSEPayload decodes one SSE data payload, reporting whether it was a
// JSON object at all; an undecodable event is skipped, as it is live.
func decodeSSEPayload(data []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil, false
	}
	return payload, true
}
