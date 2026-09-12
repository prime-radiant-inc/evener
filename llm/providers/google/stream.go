package google

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// decodeGenerateContentStream consumes the streamGenerateContent SSE stream in
// its own goroutine: it translates each chunk into llm stream events and emits
// the final accumulated Response when the model signals a finish reason. It
// owns closing the response body and the ChanStream. provider stamps every
// Response/error the decoder produces; endpointURL and material back
// llm.StampEndpointURL's fallback and credential-sanitization inputs.
func decodeGenerateContentStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, provider, endpointURL string, material llm.APILogCredentialMaterial, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	textID := "text_1"
	textStarted := false
	finished := false
	var textBuf strings.Builder
	var contentParts []llm.ContentPart
	var usage llm.Usage
	finish := llm.FinishReason{Reason: "stop"}
	var finalEvent *llm.StreamEvent

	flushTextPart := func() {
		if textBuf.Len() == 0 {
			return
		}
		contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
		textBuf.Reset()
	}

	runner := &transport.StreamRunner{
		Provider:   provider,
		Resp:       resp,
		Stream:     s,
		Attempt:    attempt,
		StatusCode: resp.StatusCode,
		FinalEvent: func() *llm.StreamEvent {
			return finalEvent
		},
		// HTTP response-byte idle is owned by ClientWithAdapterTimeout, below gzip decoding.
		Finished:      &finished,
		IncompleteMsg: provider + " stream ended without completion",
		OnEvent: func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			var raw map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&raw); err != nil {
				// Undecodable event: forward it raw and keep the stream alive
				// rather than aborting on a single malformed/unknown event.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
			}

			if _, ok := raw["error"].(map[string]any); ok {
				// In-band provider failure on an HTTP 200 stream. Decode it
				// into the typed error hierarchy and end the stream with it —
				// the raw passthrough below would drop the payload and degrade
				// the failure to the generic incomplete-stream error, hiding
				// quota/overload causes from retry logic and forensics.
				if inband := inbandStreamError(ev.Data); inband != nil {
					return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(inband, provider)}
				}
			}

			// candidates[0].content.parts
			if cands, ok := raw["candidates"].([]any); ok && len(cands) > 0 {
				if c0, ok := cands[0].(map[string]any); ok {
					if content, ok := c0["content"].(map[string]any); ok {
						if parts, ok := content["parts"].([]any); ok {
							for _, pAny := range parts {
								p, ok := pAny.(map[string]any)
								if !ok {
									continue
								}
								// Thought parts must be checked BEFORE text parts
								// because thought parts also have a "text" field.
								if thought, _ := p["thought"].(bool); thought {
									text, _ := p["text"].(string)
									if text != "" {
										flushTextPart()
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: text})
										s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
										contentParts = append(contentParts, llm.ContentPart{
											Kind: llm.ContentThinking,
											Thinking: &llm.ThinkingData{
												Text: text,
											},
										})
									}
									continue
								}
								if t, _ := p["text"].(string); t != "" {
									if !textStarted {
										textStarted = true
										s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
									}
									textBuf.WriteString(t)
									s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: t})
									continue
								}
								if fc, ok := p["functionCall"].(map[string]any); ok {
									name, _ := fc["name"].(string)
									argsAny := normalizeJSONNumbers(fc["args"])
									argsRaw, _ := json.Marshal(argsAny)
									thoughtSig := geminiThoughtSignature(p, fc)
									flushTextPart()

									id := identifier.MustNewSyntheticCallID()
									tc := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
									tcEnd := llm.ToolCallData{
										ID:               id,
										Name:             name,
										Arguments:        argsRaw,
										Type:             "function",
										ThoughtSignature: thoughtSig,
									}
									s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tcEnd})

									// Preserve tool call in the accumulated response.
									contentParts = append(contentParts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &tcEnd})
								}
							}
						}
					}
					// Parse groundingMetadata for web search results (mirrors fromGeminiResponse).
					if gm, ok := c0["groundingMetadata"].(map[string]any); ok && len(gm) > 0 {
						query := ""
						if wq, ok := gm["webSearchQueries"].([]any); ok && len(wq) > 0 {
							qs := make([]string, 0, len(wq))
							for _, q := range wq {
								if s, ok := q.(string); ok {
									qs = append(qs, s)
								}
							}
							query = strings.Join(qs, "; ")
						}
						gmRaw, _ := json.Marshal(gm)
						contentParts = append(contentParts, llm.ContentPart{
							Kind: llm.ContentWebSearch,
							WebSearch: &llm.WebSearchData{
								Query: query,
								Raw:   gmRaw,
							},
						})
					}
					if fr, _ := c0["finishReason"].(string); fr != "" {
						finish = llm.NormalizeFinishReason("google", fr)
						if textStarted {
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
							textStarted = false
						}
						// Finish on explicit finishReason chunk.
						flushTextPart()
						// Usage commonly rides the finish chunk (the dominant Gemini
						// shape) rather than arriving in a separate earlier chunk.
						// This branch returns before the separate-chunk usageMetadata
						// parse below, so capture the finish chunk's usage here too.
						if um, ok := raw["usageMetadata"].(map[string]any); ok {
							usage = parseUsage(um)
						}
						msg := llm.Message{Role: llm.RoleAssistant, Content: contentParts}
						r := llm.Response{
							Provider: provider,
							Model:    req.Model,
							Message:  msg,
							Finish:   finish,
							Usage:    usage,
							Raw:      raw,
						}
						llm.StampEndpointURL(&r, llm.FinalResponseEndpointURL(resp, endpointURL), material)
						if len(r.ToolCalls()) > 0 {
							r.Finish = llm.FinishReason{Reason: "tool_calls", Raw: r.Finish.Raw}
						}
						// NormalizeFinishReason always supplies a non-empty canonical reason;
						// tool calls override it with the provider-neutral continuation reason.
						invariant.Hold(r.Finish.Reason != "", "%s finished response has an empty finish reason", provider)
						rp := r
						event := llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp}
						if attempt.Active() {
							finalEvent = &event
						} else {
							s.Send(event)
						}
						finished = true
						cancel()
						return nil
					}
				}
			}

			if um, ok := raw["usageMetadata"].(map[string]any); ok {
				usage = parseUsage(um)
			}

			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: raw})
			return nil
		},
	}
	runner.Run(sctx)
}

func tokenCountInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	default:
		return 0
	}
}
