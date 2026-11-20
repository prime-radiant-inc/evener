package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
)

// ExtractRecordedResponse offline re-extracts the canonical llm.Response from
// a stored API-log response body recorded for Google Interactions
// (google_interactions JSON or google_interactions_stream SSE) for
// evener-doctor's `apilog --recompute`.
func ExtractRecordedResponse(body []byte, requestedModel string) (llm.Response, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return llm.Response{}, errors.New("google: empty recorded response body")
	}
	if trimmed[0] == '{' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			return llm.Response{}, fmt.Errorf("google: decode recorded interactions body: %w", err)
		}
		if _, hasChoices := raw["choices"]; hasChoices {
			return llm.Response{}, errors.New("google: recorded body is a Chat Completions shape, not Interactions API")
		}
		if _, hasCandidates := raw["candidates"]; hasCandidates {
			return llm.Response{}, errors.New("google: recorded body is a generateContent shape, not Interactions API")
		}
		return fromInteraction(raw, requestedModel, trimmed, "", "")
	}
	return replayRecordedInteractionStream(trimmed, requestedModel)
}

func replayRecordedInteractionStream(body []byte, requestedModel string) (llm.Response, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	stream := llm.NewChanStream(nil)
	go decodeInteractionStream(ctx, cancel, resp, stream, llm.Request{Model: requestedModel}, "", "", "", llm.APILogCredentialMaterial{}, nil, false)

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
			return llm.Response{}, fmt.Errorf("google: replay recorded interactions stream: %w", streamErr)
		}
		return llm.Response{}, errors.New("google: recorded interactions stream body ended without completion")
	}
	if final.Model == "" {
		final.Model = requestedModel
	}
	return *final, nil
}
