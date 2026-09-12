package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// decodeMessagesStream consumes the messages SSE stream in its own
// goroutine: it translates each event into llm stream events and emits the
// final accumulated Response on message_stop. It owns closing the response
// body and the ChanStream. provider stamps every Response/error the decoder
// produces; endpointURL and material back llm.StampEndpointURL's fallback
// and credential-sanitization inputs.
func decodeMessagesStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, provider, endpointURL string, material llm.APILogCredentialMaterial, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	finished := false
	type blockState struct {
		typ      string
		rawStart map[string]any // raw content_block from content_block_start

		// text
		textID      string
		textStarted bool
		text        strings.Builder

		// tool_use
		toolID      string
		toolName    string
		toolStarted bool
		toolArgs    strings.Builder

		// thinking / redacted_thinking
		thinkingStarted bool
		thinking        strings.Builder
		signature       strings.Builder
		redacted        bool
	}
	blocks := map[int]*blockState{}
	maxIdx := -1

	// reasoningBlockIdx tracks the block index whose reasoning deltas were last
	// emitted. When thinking spans multiple separate content blocks, a blank line
	// is inserted between them (mirrors the OpenAI summary-part behavior) so the
	// live "thinking" view stays readable. -1 means no reasoning emitted yet.
	reasoningBlockIdx := -1
	// emitReasoningDelta streams a reasoning delta, inserting a section break
	// before the first delta of a thinking block that differs from the previous.
	emitReasoningDelta := func(idx int, delta string) {
		if reasoningBlockIdx != -1 && idx != reasoningBlockIdx {
			s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "\n\n"})
		}
		reasoningBlockIdx = idx
		s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
	}

	getBlock := func(idx int) *blockState {
		st := blocks[idx]
		if st == nil {
			st = &blockState{}
			blocks[idx] = st
		}
		if idx > maxIdx {
			maxIdx = idx
		}
		return st
	}

	var usage llm.Usage
	finish := llm.FinishReason{Reason: "stop"}
	var refusalWarn *llm.Warning
	var msgID string
	var actualModel string
	var rawMessage map[string]any
	var finalEvent *llm.StreamEvent

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
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				// Undecodable event: forward it raw and keep the stream alive
				// rather than aborting on a single malformed/unknown event.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
			}

			switch ev.Event {
			case "message_start":
				if msgAny, ok := payload["message"].(map[string]any); ok {
					if id, _ := msgAny["id"].(string); id != "" {
						msgID = id
					}
					if m, _ := msgAny["model"].(string); m != "" {
						actualModel = m
					}
					rawMessage = msgAny
					if u, ok := msgAny["usage"].(map[string]any); ok {
						usage = parseUsage(u)
					}
				}
			case "content_block_start":
				idx := llm.IntFromAny(payload["index"])
				cb, _ := payload["content_block"].(map[string]any)
				typ, _ := cb["type"].(string)
				st := getBlock(idx)
				st.typ = typ
				st.rawStart = cb
				if cb, ok := payload["content_block"].(map[string]any); ok {
					switch typ {
					case "text":
						if st.textID == "" {
							st.textID = fmt.Sprintf("text_%d", idx)
						}
						if !st.textStarted {
							st.textStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
						}
					case "tool_use":
						st.toolID, _ = cb["id"].(string)
						st.toolName, _ = cb["name"].(string)
						// Note: content_block_start always has input:{} as a placeholder.
						// Actual arguments arrive via input_json_delta events; capturing
						// the placeholder here would corrupt them (e.g. {}{"city":"Paris"}).
						if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
						}
					case "thinking", "redacted_thinking":
						st.redacted = (typ == "redacted_thinking")
						if sig, _ := cb["signature"].(string); sig != "" && st.signature.Len() == 0 {
							st.signature.WriteString(sig)
						}
						if !st.thinkingStarted {
							st.thinkingStarted = true
							s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
						}
						// Some implementations may include initial thinking in the start block.
						t, _ := cb["thinking"].(string)
						if t == "" {
							t, _ = cb["text"].(string)
						}
						if t == "" {
							t, _ = cb["data"].(string)
						}
						if t != "" {
							st.thinking.WriteString(t)
							emitReasoningDelta(idx, t)
						}
					case "server_tool_use":
						st.toolID, _ = cb["id"].(string)
						st.toolName, _ = cb["name"].(string)
					case "web_search_tool_result":
						// raw payload stored in st.rawStart above
					}
				}
			case "content_block_delta":
				idx := llm.IntFromAny(payload["index"])
				st := getBlock(idx)
				if d, ok := payload["delta"].(map[string]any); ok {
					switch typ, _ := d["type"].(string); typ {
					case "text_delta":
						if delta, _ := d["text"].(string); delta != "" {
							if st.textID == "" {
								st.textID = fmt.Sprintf("text_%d", idx)
							}
							if !st.textStarted {
								st.textStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: st.textID})
							}
							st.text.WriteString(delta)
							s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: st.textID, Delta: delta})
						}
					case "input_json_delta":
						if delta, _ := d["partial_json"].(string); delta != "" {
							st.toolArgs.WriteString(delta)
							if !st.toolStarted && strings.TrimSpace(st.toolID) != "" {
								st.toolStarted = true
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
							}
							if strings.TrimSpace(st.toolID) != "" {
								// The delta carries only this event's fragment: consumers
								// (llm.StreamAccumulator, agent's consumeModelStream) build
								// the args by appending deltas, and the complete args travel
								// on ToolCallEnd.
								tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(delta), Type: "function"}
								s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
							}
						}
					case "thinking_delta":
						delta, _ := d["thinking"].(string)
						if delta == "" {
							delta, _ = d["text"].(string)
						}
						if delta != "" {
							if !st.thinkingStarted {
								st.thinkingStarted = true
								s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
							}
							st.thinking.WriteString(delta)
							emitReasoningDelta(idx, delta)
						}
					case "signature_delta":
						if delta, _ := d["signature"].(string); delta != "" {
							st.signature.WriteString(delta)
						}
					}
				}
			case "content_block_stop":
				idx := llm.IntFromAny(payload["index"])
				st := blocks[idx]
				if st == nil {
					return nil
				}
				switch st.typ {
				case "text":
					if st.textStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: st.textID})
						st.textStarted = false
					}
				case "tool_use":
					if strings.TrimSpace(st.toolID) != "" {
						if !st.toolStarted {
							st.toolStarted = true
							tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Type: "function"}
							s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
						}
						tc := llm.ToolCallData{ID: st.toolID, Name: st.toolName, Arguments: []byte(st.toolArgs.String()), Type: "function"}
						s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
						st.toolStarted = false
					}
				case "thinking", "redacted_thinking":
					if st.thinkingStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
						st.thinkingStarted = false
					}
				}
			case "message_delta":
				// The streaming message_delta event nests the stop reason under
				// "delta" ({"delta":{"stop_reason":...}}), unlike the non-streaming
				// message object which carries it at the top level. Read the nested
				// path so a streamed response reports its true finish reason (e.g.
				// max_tokens -> length) instead of always defaulting to stop.
				if delta, ok := payload["delta"].(map[string]any); ok {
					if sr, _ := delta["stop_reason"].(string); sr != "" {
						finish = llm.NormalizeFinishReason("anthropic", sr)
					}
					// Claude 5+ attaches stop_details to a "refusal" stop
					// reason; capture it for the final Response's warnings.
					if details, ok := delta["stop_details"].(map[string]any); ok {
						if w := refusalWarning(details); w != nil {
							refusalWarn = w
						}
					}
				}
				if u, ok := payload["usage"].(map[string]any); ok {
					u2 := parseUsage(u)
					if u2.OutputTokens > 0 {
						usage.OutputTokens = u2.OutputTokens
					}
					if u2.InputTokens > 0 {
						usage.InputTokens = u2.InputTokens
					}
				}
			case "message_stop":
				var parts []llm.ContentPart
				for i := 0; i <= maxIdx; i++ {
					st := blocks[i]
					if st == nil {
						continue
					}
					switch st.typ {
					case "text":
						if st.text.Len() > 0 {
							parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: st.text.String()})
						}
					case "tool_use":
						if strings.TrimSpace(st.toolID) != "" {
							args := st.toolArgs.String()
							if args == "" {
								args = "{}"
							}
							// Empty tool arguments are normalized to "{}" just above, so an
							// assembled tool call always carries a non-empty arguments string.
							invariant.Hold(args != "", "anthropic assembled tool call %q has empty arguments", st.toolID)
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentToolCall,
								ToolCall: &llm.ToolCallData{
									ID:        st.toolID,
									Name:      st.toolName,
									Arguments: json.RawMessage(args),
									Type:      "function",
								},
							})
						}
					case "thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentThinking,
								Thinking: &llm.ThinkingData{
									Text:      st.thinking.String(),
									Signature: st.signature.String(),
									Redacted:  false,
								},
							})
						}
					case "redacted_thinking":
						if st.thinking.Len() > 0 {
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentRedThinking,
								Thinking: &llm.ThinkingData{
									Text:     st.thinking.String(),
									Redacted: true,
								},
							})
						}
					case "server_tool_use":
						if st.rawStart != nil {
							query := ""
							if input, _ := st.rawStart["input"].(map[string]any); input != nil {
								query, _ = input["query"].(string)
							}
							raw, _ := json.Marshal(st.rawStart)
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentWebSearch,
								WebSearch: &llm.WebSearchData{
									Query: query,
									Raw:   raw,
								},
							})
						}
					case "web_search_tool_result":
						if st.rawStart != nil {
							raw, _ := json.Marshal(st.rawStart)
							parts = append(parts, llm.ContentPart{
								Kind: llm.ContentWebSearch,
								WebSearch: &llm.WebSearchData{
									Raw: raw,
								},
							})
						}
					}
				}

				msg := llm.Message{Role: llm.RoleAssistant, Content: parts}
				model := actualModel
				if model == "" {
					model = req.Model
				}
				r := llm.Response{
					ID:       msgID,
					Provider: provider,
					Model:    model,
					Message:  msg,
					Finish:   finish,
					Usage:    usage,
					Raw:      rawMessage,
				}
				if refusalWarn != nil {
					r.Warnings = append(r.Warnings, *refusalWarn)
				}
				llm.StampEndpointURL(&r, llm.FinalResponseEndpointURL(resp, endpointURL), material)
				// See the matching comment in fromAnthropicResponse (response.go):
				// only fall back to "tool_calls" when message_delta's stop_reason
				// was missing/empty, never when it already reported something more
				// specific like "max_tokens" (kata mmr2 — a truncated tool call
				// must not be reported as a normal tool-calls finish).
				if len(r.ToolCalls()) > 0 && r.Finish.Reason == llm.FinishReasonStop {
					r.Finish = llm.FinishReason{Reason: llm.FinishReasonToolCalls, Raw: "tool_use"}
				}

				// Best-effort thinking-token estimate from visible thinking content,
				// only when provider didn't supply a native ReasoningTokens count.
				// Informational only — never enters the billing path.
				if r.Usage.ReasoningTokens == nil && r.Usage.ReasoningTokensEstimated == nil {
					if est := estimateThinkingTokens(parts); est > 0 {
						e := est
						r.Usage.ReasoningTokensEstimated = &e
					}
				}

				rp := r
				event := llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp}
				if attempt.Active() {
					finalEvent = &event
				} else {
					s.Send(event)
				}
				finished = true
				cancel()
			case "error":
				// Anthropic reports a mid-stream failure as a structured error
				// event on an HTTP 200 stream. Decode it into the typed error
				// hierarchy and end the stream with it — the raw passthrough
				// below would drop the payload and degrade the failure to the
				// generic incomplete-stream error, hiding the
				// overload/rate-limit/auth cause from retry logic and forensics.
				return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(inbandStreamError(payload), provider)}
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		},
	}
	runner.Run(sctx)
}

func intFromAny(v any) int {
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
