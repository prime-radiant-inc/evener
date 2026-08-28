package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
)

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload's "output" field or from
// decodeStream's accumulated response.output_item.done events, so both
// callers share this one walk.
func responseContentFromOutputItems(out []any) []llm.ContentPart {
	var content []llm.ContentPart
	for _, itemAny := range out {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "message":
			phase, _ := item["phase"].(string)
			// content: [{type:"output_text", text:"..."}]
			if itemContent, ok := item["content"].([]any); ok {
				for _, cAny := range itemContent {
					c, ok := cAny.(map[string]any)
					if !ok {
						continue
					}
					ct, _ := c["type"].(string)
					if ct == "output_text" {
						text, _ := c["text"].(string)
						if text != "" || phase != "" {
							content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: text, Phase: phase})
						}
					}
				}
			}
		case "function_call":
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			callID, _ := item["call_id"].(string)
			itemID, _ := item["id"].(string)
			if itemID == "" {
				itemID, _ = item["item_id"].(string)
			}
			content = append(content, llm.ContentPart{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        callID,
					ItemID:    itemID,
					Name:      name,
					Arguments: json.RawMessage(args),
					Type:      "function",
				},
			})
		case "web_search_call":
			query := ""
			if action, _ := item["action"].(map[string]any); action != nil {
				query, _ = action["query"].(string)
			}
			raw, _ := json.Marshal(item)
			content = append(content, llm.ContentPart{
				Kind: llm.ContentWebSearch,
				WebSearch: &llm.WebSearchData{
					Query: query,
					Raw:   raw,
				},
			})
		case "reasoning":
			id, _ := item["id"].(string)
			encryptedContent, _ := item["encrypted_content"].(string)
			// OpenAI returns reasoning as encrypted_content plus summaries;
			// gateways that expose the raw thinking put reasoning_text parts
			// in content instead. On this path the raw text feeds display and
			// the transcript; toResponsesInput replays a reasoning item only
			// when it has encrypted_content, so the text never returns to
			// this API.
			text := strings.Join(parseReasoningSummary(item["content"]), reasoningPartSeparator)
			if encryptedContent != "" || text != "" {
				content = append(content, llm.ContentPart{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						ID:               id,
						Text:             text,
						EncryptedContent: encryptedContent,
						Summary:          parseReasoningSummary(item["summary"]),
					},
				})
			}
		default:
			// ignore
		}
	}
	return content
}

// settleResponsesTerminalOutput decides the settled message content and
// finish reason for a Responses-API terminal (response.completed) payload,
// given the output items accumulated from the stream en route. When the
// terminal payload's own "output" array is non-empty it is authoritative
// (the provider's settled truth); when it's empty but the stream accumulated
// real items, those are synthesized in its place (observed on affected
// sessions: the terminal payload carries no output even though earlier
// response.output_item.done events in the same stream carried real content).
// Shared by the live streaming decoder (decodeStream) and offline
// recomputation so both apply the identical terminal-wins rule.
func settleResponsesTerminalOutput(r *llm.Response, rawResp map[string]any, accumulatedOutput []any) {
	terminalOutput, _ := rawResp["output"].([]any)
	switch {
	case len(terminalOutput) == 0 && len(accumulatedOutput) > 0:
		// The terminal payload carries no output even though the stream's
		// output_item.done events carried real content (observed on
		// affected sessions). Synthesize the settled message from what the
		// stream actually sent, reusing fromResponses' item-walk.
		r.Message.Content = responseContentFromOutputItems(accumulatedOutput)
		if status, _ := rawResp["status"].(string); status != "incomplete" {
			if len(r.ToolCalls()) > 0 {
				r.Finish = llm.FinishReason{Reason: "tool_calls"}
			} else {
				r.Finish = llm.FinishReason{Reason: "stop"}
			}
		}
	case len(terminalOutput) > 0 && len(accumulatedOutput) > 0 && len(terminalOutput) != len(accumulatedOutput):
		// Terminal output is authoritative when non-empty, but a count
		// mismatch against what the stream accumulated is worth surfacing.
		r.Warnings = append(r.Warnings, llm.Warning{
			Code:    "responses_output_item_count_mismatch",
			Message: fmt.Sprintf("terminal output items=%d differ from accumulated stream items=%d", len(terminalOutput), len(accumulatedOutput)),
		})
	}
}

// fromResponses maps a Responses API JSON object into an llm.Response.
// Provider is left as "openai": the non-streaming caller (complete.go, via
// protocolhttp.Do) stamps it to res.Instance after this returns, and the
// streaming caller (stream.go's decodeStream) stamps it itself since it
// calls this directly, outside Do.
func fromResponses(raw map[string]any, requestedModel string) llm.Response {
	// Best-effort mapping. OpenAI Responses output is a list of typed items.
	r := llm.Response{
		Provider: "openai",
		Model:    requestedModel,
		Raw:      raw,
	}
	if id, _ := raw["id"].(string); id != "" {
		r.ID = id
	}
	if m, _ := raw["model"].(string); m != "" {
		r.Model = m
	}

	msg := llm.Message{Role: llm.RoleAssistant}

	// Parse output items.
	if out, ok := raw["output"].([]any); ok {
		msg.Content = responseContentFromOutputItems(out)
	}

	r.Message = msg

	// Check Responses API status/incomplete_details for finish reason.
	status, _ := raw["status"].(string)
	switch {
	case status == "incomplete":
		reason := "length" // default for incomplete
		if details, ok := raw["incomplete_details"].(map[string]any); ok {
			if dr, _ := details["reason"].(string); dr != "" {
				switch dr {
				case "max_output_tokens":
					reason = "length"
				case "content_filter":
					reason = "content_filter"
				default:
					reason = "other"
				}
			}
		}
		r.Finish = llm.FinishReason{Reason: reason, Raw: status}
	case len(r.ToolCalls()) > 0:
		r.Finish = llm.FinishReason{Reason: "tool_calls"}
	default:
		r.Finish = llm.FinishReason{Reason: "stop"}
	}

	// usage
	if u, ok := raw["usage"].(map[string]any); ok {
		r.Usage = parseUsage(u)
	}
	// The status switch above assigns one of a fixed set of non-empty reasons in
	// every branch, so a decoded response always carries a finish reason.
	invariant.Hold(r.Finish.Reason != "", "fromResponses produced an empty finish reason (status %q)", status)
	return r
}

// parseUsage maps a Responses API usage object into an llm.Usage.
func parseUsage(u map[string]any) llm.Usage {
	// OpenAI's Responses API reports input_tokens as total-including-cached,
	// with cached_tokens a subset in input_tokens_details. The llm.Usage
	// invariant is that InputTokens means new uncached input only, so we
	// subtract cached here.
	rawInput := llm.IntFromAny(u["input_tokens"])
	output := llm.IntFromAny(u["output_tokens"])
	var cachedRead int
	if inDetails, ok := u["input_tokens_details"].(map[string]any); ok {
		cachedRead = llm.IntFromAny(inDetails["cached_tokens"])
	}
	uncachedInput := max(rawInput-cachedRead, 0)
	usage := llm.Usage{
		InputTokens:  uncachedInput,
		OutputTokens: output,
		TotalTokens:  llm.IntFromAny(u["total_tokens"]),
		Raw:          u,
	}
	if outDetails, ok := u["output_tokens_details"].(map[string]any); ok {
		rt := llm.IntFromAny(outDetails["reasoning_tokens"])
		usage.ReasoningTokens = &rt
	}
	if _, ok := u["input_tokens_details"].(map[string]any); ok {
		ct := cachedRead
		usage.CacheReadTokens = &ct
	}
	// InputTokens is new uncached input; the input-minus-cached subtraction above
	// is clamped at zero, so a negative value would mean that clamp regressed.
	invariant.Hold(usage.InputTokens >= 0, "responses parseUsage produced negative InputTokens: %d", usage.InputTokens)
	return usage
}
