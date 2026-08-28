package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response
// from a stored API-log response body recorded for the
// openai_chat_completions endpoint family, for evener-doctor's `apilog
// --recompute`. body is the raw, decoded response body exactly as it was
// received on the wire: a single Chat Completions JSON object (Complete's
// path) goes through fromChatCompletionResponse, and a `data:` chunk stream
// (Stream's path) is replayed through decodeStream. Reusing the live
// decoders is the point — a second hand-rolled parser would silently
// diverge from what the live path recorded (reasoning_content and the other
// fields fromChatCompletionResponse already covers). requestedModel is used
// as a fallback when the recorded payload omits its own model field.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("chatcompletions: empty recorded response body")
	}
	if trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			return llm.Response{}, fmt.Errorf("chatcompletions: decode recorded chat completions body: %w", err)
		}
		resp, err := fromChatCompletionResponse(raw, nil)
		if err != nil {
			return llm.Response{}, err
		}
		return withRequestedModel(resp, requestedModel), nil
	}
	return replayRecordedStream(trimmed, requestedModel)
}

// replayRecordedStream feeds a stored chunk stream to decodeStream, the live
// stream decoder, and returns the Response its finish event carries. The
// recorded body stands in for the live response body and the attempt is
// absent, so the decoder publishes the finish event directly.
func replayRecordedStream(body []byte, requestedModel string) (llm.Response, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	stream := llm.NewChanStream(nil)
	go decodeStream(ctx, cancel, resp, stream, llm.Request{Model: requestedModel}, registry.Resolved{}, &protocolhttp.Result{}, nil)

	var final *llm.Response
	var streamErr error
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventFinish:
			final = ev.Response
		case llm.StreamEventError:
			streamErr = ev.Err
		}
	}
	if final == nil {
		if streamErr != nil {
			return llm.Response{}, fmt.Errorf("chatcompletions: replay recorded chat completions stream: %w", streamErr)
		}
		return llm.Response{}, errors.New("chatcompletions: recorded chat completions stream body has no [DONE] event")
	}
	return withRequestedModel(*final, requestedModel), nil
}

// withRequestedModel fills in the model the request named when the recorded
// payload carried none of its own.
func withRequestedModel(resp llm.Response, requestedModel string) llm.Response {
	if resp.Model == "" {
		resp.Model = requestedModel
	}
	return resp
}
