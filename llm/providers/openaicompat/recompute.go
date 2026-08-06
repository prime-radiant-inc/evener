package openaicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/serf/llm"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response
// from a stored API-log response body recorded for the
// openai_compatible_chat_completions endpoint family (see
// completeViaChatCompletions), for serf-doctor's `apilog --recompute`. It
// reuses fromChatCompletionResponse -- the live non-streamed parser for
// this family -- rather than a second hand-rolled JSON decoder, so
// recompute output can't silently diverge from what the live path would
// have recorded (e.g. reasoning_content and the other quirk-handled fields
// fromChatCompletionResponse already covers).
//
// body must be a single Chat Completions JSON response object; this family
// SSE-streams too (streamViaChatCompletions), but that live decoder is a
// distinct implementation from this non-streamed parser and isn't reused
// here, so SSE bodies under this family are rejected rather than
// mis-parsed. requestedModel is used as a fallback when the recorded
// payload omits its own model field.
//
// openai.ExtractRecordedResponse and openai.ExtractRecordedChatCompletionsResponse
// cover the Responses API and this adapter's own Chat Completions fallback
// respectively; openaicompat imports openai (for responsesAdapter), so this
// entry point lives here rather than being folded into that package.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("openai-compatible: empty recorded response body")
	}
	if trimmed[0] != '{' {
		return llm.Response{}, errors.New("openai-compatible: recorded openai_compatible_chat_completions body is not JSON -- SSE recomputation for this family is not supported (its live stream decoder is a separate implementation from fromChatCompletionResponse)")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return llm.Response{}, fmt.Errorf("openai-compatible: decode recorded chat completions body: %w", err)
	}
	resp, err := fromChatCompletionResponse(raw, ProviderQuirks{})
	if err != nil {
		return llm.Response{}, err
	}
	if resp.Model == "" {
		resp.Model = requestedModel
	}
	return resp, nil
}
