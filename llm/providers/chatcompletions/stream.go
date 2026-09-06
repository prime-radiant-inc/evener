package chatcompletions

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// decodeStream consumes the Chat Completions SSE stream in its own goroutine: it
// translates each chunk into llm stream events and emits the final accumulated
// Response on [DONE]. It owns closing the response body and the ChanStream.
func decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, res registry.Resolved, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
	rl := llm.ParseRateLimitHeaders(resp.Header)
	defer func() {
		_ = resp.Body.Close()
		cancel()
		s.CloseSend()
	}()

	// Track tool call state for streaming.
	type toolCallState struct {
		id   string
		name string
		args strings.Builder
	}
	toolCalls := map[int]*toolCallState{}
	var textStarted bool
	var textBuf strings.Builder
	var reasoningStarted bool
	var reasoningBuf strings.Builder
	// reasoningField remembers which wire field the first reasoning delta
	// arrived on so replay can route thinking back to the same field.
	var reasoningField string
	// encryptedDetails accumulates opaque reasoning.encrypted items (OpenRouter
	// Gemini/o-series) in arrival order; they ride the thinking part's
	// EncryptedContent so the reasoning chain survives replay.
	var encryptedDetails []map[string]any
	var responseID string
	var model string
	var finishReason string
	var usage *llm.Usage
	// sawTopLevelUsage gives top-level usage strict precedence across the
	// WHOLE stream (matching the non-stream path), not just within a chunk —
	// a mixed emitter must not have late choice-level numbers overwrite
	// earlier top-level ones.
	var sawTopLevelUsage bool
	finished := false
	var finalEvent *llm.StreamEvent

	runner := &transport.StreamRunner{
		Provider:   res.Instance,
		Resp:       resp,
		Stream:     s,
		Attempt:    attempt,
		StatusCode: resp.StatusCode,
		FinalEvent: func() *llm.StreamEvent {
			return finalEvent
		},
		// HTTP response-byte idle is owned by ClientWithAdapterTimeout, below gzip decoding.
		Finished:      &finished,
		IncompleteMsg: res.Instance + " stream ended without completion",
		OnEvent: func(ev llm.SSEEvent) error {
			data := string(ev.Data)
			if data == "[DONE]" {
				finished = true

				// Close reasoning if still open.
				if reasoningStarted && !textStarted && len(toolCalls) == 0 {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}

				// Collect and emit tool call end events in wire (delta-index)
				// order. The state is keyed in a map, whose iteration order is
				// randomized; sort by index so parallel tool calls assemble
				// deterministically.
				var completedToolCalls []llm.ToolCallData
				for _, idx := range slices.Sorted(maps.Keys(toolCalls)) {
					tc := toolCalls[idx]
					rescuedArgs := rescueClaudeXMLArgs(tc.args.String())
					tcd := llm.ToolCallData{
						ID:        tc.id,
						Name:      tc.name,
						Arguments: json.RawMessage(rescuedArgs),
						Type:      "function",
					}
					completedToolCalls = append(completedToolCalls, tcd)
					s.Send(llm.StreamEvent{
						Type:     llm.StreamEventToolCallEnd,
						ToolCall: &tcd,
					})
					delete(toolCalls, idx)
				}

				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "text_0"})
				}

				// Build final response.
				msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
				if reasoningBuf.Len() > 0 || len(encryptedDetails) > 0 {
					td := &llm.ThinkingData{Text: reasoningBuf.String(), Signature: reasoningField}
					if len(encryptedDetails) > 0 {
						if b, err := json.Marshal(encryptedDetails); err == nil {
							td.EncryptedContent = string(b)
						}
					}
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentThinking,
						Thinking: td,
					})
				}
				if textBuf.Len() > 0 {
					msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
				}
				for i := range completedToolCalls {
					msg.Content = append(msg.Content, llm.ContentPart{
						Kind:     llm.ContentToolCall,
						ToolCall: &completedToolCalls[i],
					})
				}

				rawFinish := finishReason
				mappedFinish := mapFinishReason(res.Caps.FinishReasonMap, rawFinish)
				var finish llm.FinishReason
				if mappedFinish == rawFinish {
					finish = llm.NormalizeFinishReason("", rawFinish)
				} else {
					finish = llm.FinishReason{Reason: mappedFinish, Raw: rawFinish}
				}

				finalResp := &llm.Response{
					ID:        responseID,
					Provider:  res.Instance,
					Model:     model,
					Message:   msg,
					Finish:    finish,
					RateLimit: rl,
				}
				if usage != nil {
					finalResp.Usage = *usage
				}
				if finalResp.Usage.ReasoningTokens == nil && finalResp.Usage.ReasoningTokensEstimated == nil {
					if est := estimateThinkingFromBuf(reasoningBuf.Len()); est > 0 {
						e := est
						finalResp.Usage.ReasoningTokensEstimated = &e
					}
				}
				llm.StampEndpointURL(finalResp, r.EndpointURL, r.Material)
				event := llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &finish,
					Usage:        usage,
					Response:     finalResp,
				}
				if attempt.Active() {
					finalEvent = &event
				} else {
					s.Send(event)
				}
				return nil
			}

			var chunk chatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				// Skip a single malformed chunk and keep the stream alive;
				// returning the error would abort the whole stream.
				return nil //nolint:nilerr // unparseable chunk is intentionally skipped, not fatal
			}
			if chunk.Error != nil {
				// In-band provider failure on an HTTP 200 stream. Decode it
				// into the typed error hierarchy and end the stream with it —
				// falling through would drop the payload and degrade the
				// failure to the generic incomplete-stream error, hiding
				// rate-limit/quota/upstream causes from retry and settlement
				// logic.
				var raw map[string]any
				_ = json.Unmarshal([]byte(data), &raw)
				msg := strings.TrimSpace(chunk.Error.Message)
				if msg == "" {
					msg = "provider reported an in-band stream error"
				}
				typedErr := llm.ErrorFromHTTPStatus(res.Instance,
					chunk.Error.StatusCode(), "chat.completions(stream): "+msg, raw, nil)
				return &transport.FatalStreamError{Err: typedErr}
			}
			if chunk.ID != "" {
				responseID = chunk.ID
			}
			if chunk.Model != "" {
				model = chunk.Model
			}
			if chunk.Usage != nil {
				u := openaichat.ParseChatUsage(chunk.Usage)
				usage = &u
				sawTopLevelUsage = true
			}

			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]

			// Usage fallback: Moonshot/Kimi report usage on choices[0].usage
			// instead of the top-level chunk usage. Top-level wins.
			if chunk.Usage == nil && choice.Usage != nil && !sawTopLevelUsage {
				u := openaichat.ParseChatUsage(choice.Usage)
				usage = &u
			}

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

			// Accumulate opaque encrypted reasoning items (they carry no text,
			// so reasoningFromDelta skips them).
			encryptedDetails = append(encryptedDetails, collectEncryptedDetails(choice.Delta.ReasoningDetails)...)

			// Reasoning delta — providers vary the field name; take the first
			// non-empty variant per chunk (duplicated identical fields must
			// not double the text) and remember the field for replay.
			if delta, field := reasoningFromDelta(choice.Delta); delta != "" {
				if !reasoningStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
					reasoningStarted = true
					reasoningField = field
				}
				reasoningBuf.WriteString(delta)
				s.Send(llm.StreamEvent{
					Type:           llm.StreamEventReasoningDelta,
					ReasoningDelta: delta,
				})
			}

			// Text delta — close reasoning first if transitioning.
			if choice.Delta.Content != "" {
				if reasoningStarted && !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
				}
				if !textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "text_0"})
					textStarted = true
				}
				textBuf.WriteString(choice.Delta.Content)
				s.Send(llm.StreamEvent{
					Type:   llm.StreamEventTextDelta,
					TextID: "text_0",
					Delta:  choice.Delta.Content,
				})
			}

			// Tool call deltas.
			for _, tc := range choice.Delta.ToolCalls {
				state, exists := toolCalls[tc.Index]
				if !exists {
					// Close reasoning before the first tool call if needed.
					if reasoningStarted && !textStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
						reasoningStarted = false // Prevent double-close in [DONE].
					}
					state = &toolCallState{
						id:   tc.ID,
						name: tc.Function.Name,
					}
					toolCalls[tc.Index] = state
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallStart,
						ToolCall: &llm.ToolCallData{
							ID:   tc.ID,
							Name: tc.Function.Name,
							Type: "function",
						},
					})
				}
				if tc.Function.Arguments != "" {
					state.args.WriteString(tc.Function.Arguments)
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallDelta,
						ToolCall: &llm.ToolCallData{
							ID:        state.id,
							Name:      state.name,
							Arguments: json.RawMessage(tc.Function.Arguments),
							Type:      "function",
						},
					})
				}
			}

			return nil
		},
	}
	runner.Run(sctx)
}
