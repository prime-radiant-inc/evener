package chatcompletions

import (
	"bytes"
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
func decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, res registry.Resolved, r *protocolhttp.Result, attempt *transport.APIAttemptCapture, warnings []llm.Warning) {
	rl := llm.ParseRateLimitHeaders(resp.Header)
	defer func() {
		_ = resp.Body.Close()
		cancel()
		s.CloseSend()
	}()

	// Track tool call state for streaming.
	type toolCallState struct {
		id               string
		name             string
		thoughtSignature string
		args             bytes.Buffer
	}
	toolCalls := map[int]*toolCallState{}
	var textStarted bool
	var textBuf strings.Builder
	var reasoningStarted bool
	// Scalar text and structured reasoning_details accumulate separately so any
	// chunk order matches the non-stream fields: the settled thinking part keeps
	// both, while the live preview emits one copy into a single pane buffer.
	var scalarBuf strings.Builder
	var scalarField string
	details := &detailAssembly{byKey: map[detailKey]*strings.Builder{}}
	// shown is the live pane text; each source's offset tracks how much of it
	// that source has already accounted for, so a mirror does not repeat bytes.
	var shown strings.Builder
	var scalarOffset, structuredOffset int
	// encryptedDetails accumulates opaque reasoning.encrypted items (OpenRouter
	// Gemini/o-series) in arrival order; they ride the thinking part's
	// EncryptedContent so the reasoning chain survives replay.
	var encryptedDetails []json.RawMessage
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

	var runner *transport.StreamRunner
	runner = &transport.StreamRunner{
		Provider:   res.Instance,
		Resp:       resp,
		Stream:     s,
		Attempt:    attempt,
		StatusCode: resp.StatusCode,
		Cancel:     cancel,
		// HTTP response-byte idle is owned by ClientWithAdapterTimeout, below gzip decoding.
		Finished:      &finished,
		IncompleteMsg: res.Instance + " stream ended without completion",
		OnEvent: func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
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
					// rescueClaudeXMLArgs returns the input unchanged when
					// no XML is present, preserving raw bytes (incl. invalid
					// UTF-8). It only re-marshals on actual Claude-XML
					// rescue (rare, intentional).
					rescuedArgs := rescueClaudeXMLArgs(tc.args.String())
					tcd := llm.ToolCallData{
						ID:               tc.id,
						Name:             tc.name,
						Arguments:        json.RawMessage(rescuedArgs),
						Type:             "function",
						ThoughtSignature: tc.thoughtSignature,
					}
					tcd.Arguments, tcd.ArgumentsSpelling = llm.NormalizeToolArguments(tcd.Arguments)
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
				display, displayField := details.display(), ""
				if display == "" {
					display, displayField = scalarBuf.String(), scalarField
				}
				if display != "" || len(encryptedDetails) > 0 {
					td := &llm.ThinkingData{Text: display, Signature: displayField}
					if b, err := json.Marshal(encryptedDetails); err == nil && len(encryptedDetails) > 0 {
						td.EncryptedContent = string(b)
					}
					if displayField == "" && scalarBuf.Len() > 0 {
						td.ScalarReasoningField, td.ScalarReasoningText = scalarField, scalarBuf.String()
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
					Warnings:  warnings,
				}
				if usage != nil {
					finalResp.Usage = *usage
				}
				if finalResp.Usage.ReasoningTokens == nil && finalResp.Usage.ReasoningTokensEstimated == nil {
					if est := estimateThinkingFromBuf(len(display)); est > 0 {
						e := est
						finalResp.Usage.ReasoningTokensEstimated = &e
					}
				}
				llm.StampEndpointURL(finalResp, r.EndpointURL, r.Material)
				// The provider may keep the body open after [DONE]; Finish stops
				// the parser before its next read.
				runner.Finish(*finalResp)
				return nil
			}

			var chunk chatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				// Keep the stream alive for a single malformed chunk, but
				// surface the undecodable bytes as a raw passthrough event
				// rather than dropping them: silently discarding the chunk
				// would make corruption indistinguishable from an empty
				// successful stream. This matches the anthropic, google and
				// responses transports.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": data}})
				return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
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
			// so the live preview skips them).
			encryptedDetails = append(encryptedDetails, collectEncryptedDetails(choice.Delta.ReasoningDetails)...)

			// Reasoning delta — providers vary the field name; take the first
			// non-empty variant per chunk (duplicated identical fields must
			// not double the text) and remember the field for replay.
			scalar, field := extractScalarReasoning(choice.Delta.ReasoningContent, choice.Delta.Reasoning, choice.Delta.ReasoningText)
			if scalar != "" {
				if scalarField == "" {
					scalarField = field
				}
				scalarBuf.WriteString(scalar)
			}
			structured := details.appendChunk(choice.Delta.ReasoningDetails)
			delta := structured
			if delta == "" {
				delta = scalar
			}
			if delta != "" {
				var send string
				if structured != "" {
					send, structuredOffset = unseen(delta, shown.String(), structuredOffset)
					if send != "" {
						shown.WriteString(send)
					}
					// The structured candidate may include this frame's scalar
					// bytes too, so account for them before a later scalar
					// mirror arrives.
					if scalar != "" {
						pending := shown.String()[min(scalarOffset, shown.Len()):]
						if pending != "" && strings.HasPrefix(scalar, pending) {
							// The frame's scalar fragment mirrors the pane and
							// extends it; the suffix is unseen content, so it
							// joins the same preview.
							extra := scalar[len(pending):]
							shown.WriteString(extra)
							scalarOffset = shown.Len()
							send += extra
						} else if s, next := unseen(scalar, shown.String(), scalarOffset); s == "" {
							scalarOffset = next
						}
					}
				} else {
					send, scalarOffset = unseen(delta, shown.String(), scalarOffset)
					if send != "" {
						shown.WriteString(send)
					}
				}
				if send != "" {
					if !reasoningStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
						reasoningStarted = true
					}
					s.Send(llm.StreamEvent{
						Type:           llm.StreamEventReasoningDelta,
						ReasoningDelta: send,
					})
				}
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
						id:               tc.ID,
						name:             tc.Function.Name,
						thoughtSignature: thoughtSignatureFromExtraContent(tc.ExtraContent),
					}
					toolCalls[tc.Index] = state
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallStart,
						ToolCall: &llm.ToolCallData{
							ID:               tc.ID,
							Name:             tc.Function.Name,
							Type:             "function",
							ThoughtSignature: state.thoughtSignature,
						},
					})
				}
				if state.thoughtSignature == "" {
					state.thoughtSignature = thoughtSignatureFromExtraContent(tc.ExtraContent)
				}
				if fragment := tc.Function.Arguments; fragment.carriesFragment() {
					// The chunk decode already handed us this delta's wire
					// token, so unescape it once here instead of decoding the
					// chunk's bytes a second time. Unescaping per delta is
					// what keeps assembly byte-exact: a fragment boundary can
					// split an escape pair or a multi-byte rune, and only the
					// concatenated content must form the full arguments.
					// A token encoding/json accepted as a JSON string always
					// unescapes, so the string-form fallback is unreachable in
					// practice; it stays so a future token rule degrades to
					// today's string-form bytes instead of dropping the delta.
					content, cerr := protocolhttp.RawStringContent(fragment.token)
					if cerr != nil {
						content = []byte(fragment.stringForm())
					}
					state.args.Write(content)
					s.Send(llm.StreamEvent{
						Type: llm.StreamEventToolCallDelta,
						ToolCall: &llm.ToolCallData{
							ID:               state.id,
							Name:             state.name,
							Arguments:        content,
							Type:             "function",
							ThoughtSignature: state.thoughtSignature,
						},
					})
				}
			}

			return nil
		},
	}
	runner.Run(sctx)
}

// detailKey identifies one reasoning_details item: the item for an index value,
// or the single implicit item that unindexed pieces belong to.
type detailKey struct {
	indexed bool
	index   float64
}

func detailKeyOf(d reasoningDetailItem) detailKey {
	if d.Index == nil {
		return detailKey{}
	}
	return detailKey{indexed: true, index: *d.Index}
}

// detailAssembly accumulates streamed reasoning_details text by item identity:
// pieces of one item concatenate with no separator, and distinct items are
// ordered by first appearance and joined by one newline in the settled display.
type detailAssembly struct {
	order []detailKey
	byKey map[detailKey]*strings.Builder
	// last is the item of the most recent text-bearing piece, across chunks;
	// a different item in a later frame still starts a new preview line.
	last    detailKey
	lastSet bool
}

// appendChunk folds one chunk's pieces into the assembly and returns the text
// that chunk contributes to the live preview. Pieces of one explicit index
// concatenate whether or not a frame boundary separates them; unindexed pieces
// inside a single array keep the provider's newline, matching the non-stream
// join. Distinct items are separated by one newline in the preview, including
// across a frame boundary.
func (a *detailAssembly) appendChunk(details []reasoningDetailItem) string {
	var out strings.Builder
	var prev detailKey
	first := true
	// unindexedSeen records that this chunk already carried unindexed text, so
	// every later unindexed piece in the same array needs its newline in the
	// implicit item even when an indexed piece arrives between them.
	unindexedSeen := false
	for _, d := range details {
		text := detailItemText(d)
		if text == "" {
			// An indexed item's identity starts at its first appearance, even
			// when that appearance carries no text: empty items contribute
			// nothing to the display, but later text must keep this position.
			key := detailKeyOf(d)
			if key.indexed && a.byKey[key] == nil {
				a.byKey[key] = &strings.Builder{}
				a.order = append(a.order, key)
			}
			continue
		}
		key := detailKeyOf(d)
		b := a.byKey[key]
		if b == nil {
			b = &strings.Builder{}
			a.byKey[key] = b
			a.order = append(a.order, key)
		}
		sameItem := out.Len() > 0 && key == prev
		switch {
		case first:
			if a.lastSet && key != a.last {
				out.WriteString("\n")
			}
		case !sameItem || !key.indexed:
			out.WriteString("\n")
		}
		out.WriteString(text)
		if !key.indexed && unindexedSeen {
			b.WriteString("\n")
		}
		b.WriteString(text)
		if !key.indexed {
			unindexedSeen = true
		}
		prev = key
		first = false
		a.last, a.lastSet = key, true
	}
	return out.String()
}

// display is the settled reasoning text: non-empty items in first-appearance
// order, joined by exactly one newline.
func (a *detailAssembly) display() string {
	texts := make([]string, 0, len(a.order))
	for _, key := range a.order {
		texts = append(texts, a.byKey[key].String())
	}
	return joinDetailTexts(texts)
}

// unseen returns the part of candidate the pane has not displayed yet, and the
// offset this source has accounted for afterwards: past the bytes the pane
// already shows, or past the candidate once it is appended.
func unseen(candidate, shown string, offset int) (send string, next int) {
	if candidate == "" {
		return "", offset
	}
	if offset > len(shown) {
		offset = len(shown)
	}
	remaining := shown[offset:]
	switch {
	case strings.HasPrefix(candidate, remaining):
		send = candidate[len(remaining):]
		return send, len(shown) + len(send)
	case strings.HasPrefix(remaining, candidate):
		return "", offset + len(candidate)
	default:
		return candidate, len(shown) + len(candidate)
	}
}
