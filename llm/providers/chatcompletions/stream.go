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
	// structuredBuf is the structured preview accumulated in arrival order and
	// mirrorBuf is the same provider text without the synthesized item
	// separators. The pane follows whichever source has more provider content
	// (newlines excluded), so a scalar mirror matches whether each separator is
	// present or absent; once the sources carry different content it keeps
	// per-frame precedence. sMatch/mMatch are the raw agreement cursors into
	// scalarBuf and mirrorBuf, and sSent/dSent the raw emission cursors into
	// scalarBuf and structuredBuf.
	var structuredBuf strings.Builder
	var mirrorBuf strings.Builder
	var scalarContent, structuredContent, sentContent int
	var sMatch, mMatch int
	var sSent, sSentContent int
	var dSent, dSentContent int
	var diverged bool
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
				scalarContent += len(scalar) - strings.Count(scalar, "\n")
			}
			structured, mirror := details.appendChunk(choice.Delta.ReasoningDetails)
			if structured != "" {
				structuredBuf.WriteString(structured)
				structuredContent += len(structured) - strings.Count(structured, "\n")
			}
			if !diverged {
				mirrorBuf.WriteString(mirror)
			}
			delta := structured
			if delta == "" {
				delta = scalar
			}
			if delta != "" {
				var send string
				if !diverged {
					s, d, mirror := scalarBuf.String(), structuredBuf.String(), mirrorBuf.String()
					// flushStructured emits the structured preview bytes beyond
					// the content already on the pane and advances the cursors.
					flushStructured := func() string {
						dSent, dSentContent = advanceContent(d, dSent, dSentContent, sentContent)
						flush := d[dSent:]
						dSent, dSentContent = len(d), structuredContent
						sentContent = structuredContent
						return flush
					}
					// Newlines the scalar carries where the mirror does not are
					// synthesized item separators and may be present or absent; the
					// mirror's own newlines are provider text and must match.
					for {
						for sMatch < len(s) && s[sMatch] == '\n' && (mMatch == len(mirror) || mirror[mMatch] != '\n') {
							sMatch++
						}
						if sMatch == len(s) || mMatch == len(mirror) {
							break
						}
						if s[sMatch] != mirror[mMatch] {
							diverged = true
							break
						}
						sMatch++
						mMatch++
					}
					assembled := false
					if diverged {
						// The provider may mirror the items in assembled order
						// instead of the preview's arrival order. Recognize that
						// mirror against the assembled text: the pane keeps the
						// arrival-order separators it already showed, and only
						// text beyond the assembled items is new.
						if order := details.assembledMirror(); order != "" {
							if extra, ok := assembledOrderExtension(s, order); ok {
								diverged, assembled = false, true
								if structuredContent > sentContent {
									send = flushStructured()
								}
								if extra != "" {
									send += extra
									sentContent += len(extra) - strings.Count(extra, "\n")
									sSent, sSentContent = len(s), scalarContent
								}
							}
						}
					}
					if !diverged && !assembled {
						// Emit the longer source's provider bytes beyond the content
						// already on the pane, once each.
						if scalarContent >= structuredContent {
							sSent, sSentContent = advanceContent(s, sSent, sSentContent, sentContent)
							send = s[sSent:]
							sSent, sSentContent = len(s), scalarContent
							sentContent = scalarContent
						} else {
							send = flushStructured()
						}
					}
				}
				if diverged {
					// Per-frame precedence: the structured candidate when the frame has
					// one, else the scalar fragment. Divergence is permanent, and a repeat
					// of the pane's suffix is still new text here.
					send = delta
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
// that chunk contributes to the live preview and the same provider text without
// the distinct-item separators the preview synthesizes. Pieces of one explicit
// index concatenate whether or not a frame boundary separates them; unindexed
// pieces inside a single array keep the provider's newline, matching the
// non-stream join. Distinct items are separated by one newline in the preview,
// including across a frame boundary; the mirror keeps that newline only between
// unindexed pieces, so a scalar mirror matches content rather than punctuation.
func (a *detailAssembly) appendChunk(details []reasoningDetailItem) (string, string) {
	var out strings.Builder
	var mirror strings.Builder
	var prev detailKey
	first := true
	// unindexedSeen records that this chunk already carried unindexed text, so
	// every later unindexed piece in the same array needs its newline in the
	// implicit item even when an indexed piece arrives between them.
	unindexedSeen := false
	for _, d := range details {
		// Encrypted blocks are opaque replay material, never display text;
		// collectEncryptedDetails carries them for continuation.
		if d.Type == "reasoning.encrypted" {
			continue
		}
		text := detailItemText(d)
		if text == "" {
			// An item's identity starts at its first appearance for text-bearing
			// entries (indexed or unindexed) and indexed placeholders, so later
			// text keeps this position. Null and encrypted entries stay out.
			key := detailKeyOf(d)
			if key.indexed || d.Type == "reasoning.text" || d.Type == "thinking" {
				if _, ok := a.byKey[key]; !ok {
					a.byKey[key] = &strings.Builder{}
					a.order = append(a.order, key)
				}
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
			mirror.WriteString("\n")
			b.WriteString("\n")
		}
		mirror.WriteString(text)
		b.WriteString(text)
		if !key.indexed {
			unindexedSeen = true
		}
		prev = key
		first = false
		a.last, a.lastSet = key, true
	}
	return out.String(), mirror.String()
}

// itemTexts returns every item's text in assembled item order.
func (a *detailAssembly) itemTexts() []string {
	texts := make([]string, 0, len(a.order))
	for _, key := range a.order {
		texts = append(texts, a.byKey[key].String())
	}
	return texts
}

// display is the settled reasoning text: non-empty items in first-appearance
// order, joined by exactly one newline.
func (a *detailAssembly) display() string {
	return joinDetailTexts(a.itemTexts())
}

// assembledMirror is the provider text of the items in assembled item order,
// without the synthesized separators display inserts between them.
func (a *detailAssembly) assembledMirror() string {
	return strings.Join(a.itemTexts(), "")
}

// assembledOrderExtension reports whether s mirrors provider text with
// synthesized item separators optional, and returns the bytes of s beyond
// provider when s extends it. Once the assembled text is consumed, anything
// left in s is text the pane has not shown.
func assembledOrderExtension(s, provider string) (string, bool) {
	i, j := 0, 0
	for j < len(provider) && i < len(s) {
		if s[i] == '\n' && provider[j] != '\n' {
			i++
			continue
		}
		if s[i] != provider[j] {
			return "", false
		}
		i++
		j++
	}
	return s[i:], true
}

// advanceContent moves a raw cursor forward over bytes until its count of
// provider (non-newline) bytes reaches content, returning the cursor and count.
func advanceContent(s string, raw, counted, content int) (int, int) {
	for raw < len(s) && counted < content {
		if s[raw] != '\n' {
			counted++
		}
		raw++
	}
	return raw, counted
}
