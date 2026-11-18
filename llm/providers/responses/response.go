package responses

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload or output_item.done events.
func responseContentFromOutputItems(out []any, body []byte, rawItems []json.RawMessage, stream *responsesOutputAccumulator) []llm.ContentPart {
	var rawBodyItems []json.RawMessage
	var rawBodyCaptured bool
	var content []llm.ContentPart
	onlyReasoningPlaceholders := stream != nil
	for itemIdx, itemAny := range out {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		if typ != "reasoning" {
			onlyReasoningPlaceholders = false
		}
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
			// Decode the raw body once; index this item's token (rawItems
			// entries win when present).
			var token json.RawMessage
			if itemIdx < len(rawItems) {
				token = rawItems[itemIdx]
			}
			if len(token) == 0 {
				if !rawBodyCaptured {
					rawBodyCaptured = true
					rawBodyItems = captureResponsesArgsRaw(body)
				}
				if itemIdx < len(rawBodyItems) {
					token = rawBodyItems[itemIdx]
				}
			}
			if rawArgs, rawOK := captureResponsesItemArgsRaw(token); rawOK && rawArgs != nil {
				// An empty raw arguments string does not establish that the call
				// has no arguments: output_item.done can report "" after deltas
				// supplied them, and the accumulated item already recovered
				// those bytes. Keep that fallback rather than overwriting it.
				if content, cerr := protocolhttp.RawStringContent(rawArgs); cerr == nil && len(content) > 0 {
					args = string(content)
				}
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
			thinking := responsesThinking(item)
			if stream != nil {
				if hasResponsesReasoning(thinking) {
					onlyReasoningPlaceholders = false
				} else {
					thinking = stream.settleReasoning(item, thinking)
				}
			}
			if thinking != nil {
				content = append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
			}
		default:
			// ignore
		}
	}
	if onlyReasoningPlaceholders {
		return stream.sparseContent(content, len(out) != 0)
	}
	return content
}

// Only a completed reasoning item with the same native ID may supply
// reasoning absent from the terminal.
func (acc *responsesOutputAccumulator) settleReasoning(item map[string]any, terminal *llm.ThinkingData) *llm.ThinkingData {
	id, _ := item["id"].(string)
	output := acc.byID[id]
	if output == nil || output.kind != "reasoning" {
		return terminal
	}
	prior := responsesThinking(output.done)
	if !hasResponsesReasoning(prior) {
		return terminal
	}
	if summary := parseReasoningSummary(item["summary"]); len(summary) > 0 {
		prior.Summary = summary
	}
	return prior
}

// An empty terminal or one containing only reasoning placeholders relies on
// observed output for its answer and calls. Named terminal placeholders admit
// only their matching reasoning. A stream of deltas alone remains available
// to the caller's accumulator, including its display-only reasoning.
func (acc *responsesOutputAccumulator) sparseContent(reasoning []llm.ContentPart, hasTerminalItems bool) []llm.ContentPart {
	if acc.completed == 0 && len(reasoning) == 0 {
		return nil
	}
	matched := make(map[*responsesOutputItem]llm.ContentPart, len(reasoning))
	var content []llm.ContentPart
	for _, part := range reasoning {
		if item := acc.byID[part.Thinking.ID]; item != nil && item.kind == "reasoning" {
			matched[item] = part
		} else {
			content = append(content, part)
		}
	}
	acc.orderOutput()
	for _, item := range acc.output {
		if hasTerminalItems && item.kind == "reasoning" {
			if part, ok := matched[item]; ok {
				content = append(content, part)
			}
			continue
		}
		if item.text.Len() > 0 {
			content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: item.text.String(), Phase: item.phase})
			continue
		}
		if item.done != nil {
			parts := responseContentFromOutputItems([]any{item.done}, nil, []json.RawMessage{item.raw}, nil)
			if item.kind == "message" && item.phase != "" {
				for i := range parts {
					parts[i].Phase = item.phase
				}
			}
			if len(parts) != 0 {
				content = append(content, parts...)
				continue
			}
		}
		if st := item.tool; st != nil {
			content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID: st.id, ItemID: st.itemID, Name: st.name, Type: "function", Arguments: st.args.Bytes(),
			}})
		}
	}
	return content
}

// Providers may omit an index on some or all items. Fill unclaimed positions
// by first appearance without allocating a slice sized by a wire index.
func (acc *responsesOutputAccumulator) orderOutput() {
	if len(acc.byIndex) == 0 {
		return
	}
	var next int64
	for _, item := range acc.output {
		if item.index < 0 {
			for acc.byIndex[next] != nil {
				next++
			}
			item.index = next
			next++
		}
	}
	slices.SortStableFunc(acc.output, func(a, b *responsesOutputItem) int { return cmp.Compare(a.index, b.index) })
}

func hasResponsesReasoning(t *llm.ThinkingData) bool {
	if t == nil {
		return false
	}
	if t.EncryptedContent != "" {
		return true
	}
	for text := range t.ResponsesReasoningTextParts() {
		if strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func responsesThinking(item map[string]any) *llm.ThinkingData {
	id, _ := item["id"].(string)
	encrypted, _ := item["encrypted_content"].(string)
	parts, _ := item["content"].([]any)
	var display strings.Builder
	var textParts []llm.ResponsesTextPart
	for _, part := range parts {
		p, _ := part.(map[string]any)
		text, ok := p["text"].(string)
		kind, _ := p["type"].(string)
		if !ok || (kind == "" && strings.TrimSpace(text) == "") {
			continue
		}
		if len(textParts) != 0 {
			display.WriteString(reasoningPartSeparator)
		}
		start := display.Len()
		display.WriteString(text)
		textParts = append(textParts, llm.ResponsesTextPart{Type: kind, Start: start, End: display.Len()})
	}
	// A blank unencrypted item leaves the streamed answer available for
	// recovery. A substantive item keeps all of its typed part boundaries.
	text := display.String()
	if encrypted == "" && strings.TrimSpace(text) == "" {
		return nil
	}
	return &llm.ThinkingData{
		ID:                 id,
		Text:               text,
		EncryptedContent:   encrypted,
		Summary:            parseReasoningSummary(item["summary"]),
		ResponsesTextParts: textParts,
	}
}

// fromResponses maps a Responses API JSON object into an llm.Response.
// body is the raw response body bytes for byte-faithful argument capture;
// nil falls back to the existing string-form path.
func fromResponses(raw map[string]any, requestedModel string, body []byte) llm.Response {
	out, _ := raw["output"].([]any)
	return responseWithContent(raw, requestedModel, responseContentFromOutputItems(out, body, nil, nil))
}

// settleResponsesTerminalOutput uses the terminal's item order and content.
// A matching completed reasoning item can fill in absent native content.
// An empty output array uses the stream's completed items and pending output.
func settleResponsesTerminalOutput(raw map[string]any, requestedModel string, body []byte, acc *responsesOutputAccumulator) llm.Response {
	out, _ := raw["output"].([]any)
	r := responseWithContent(raw, requestedModel, responseContentFromOutputItems(out, body, nil, acc))
	if n := acc.completed; len(out) > 0 && n > 0 && len(out) != n {
		r.Warnings = append(r.Warnings, llm.Warning{
			Code:    "responses_output_item_count_mismatch",
			Message: fmt.Sprintf("terminal output items=%d differ from accumulated stream items=%d", len(out), n),
		})
	}
	return r
}

func responseWithContent(raw map[string]any, requestedModel string, content []llm.ContentPart) llm.Response {
	// Best-effort mapping. OpenAI Responses output is a list of typed items.
	r := llm.Response{
		Provider: "openai",
		Model:    requestedModel,
		Raw:      raw,
		Message:  llm.Message{Role: llm.RoleAssistant, Content: content},
	}
	if id, _ := raw["id"].(string); id != "" {
		r.ID = id
	}
	if m, _ := raw["model"].(string); m != "" {
		r.Model = m
	}

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

// rawArgsBodyDecode is the focused raw-args body decode. Tests replace it to
// count focused decodes per response body.
var rawArgsBodyDecode = json.Unmarshal

// captureResponsesArgsRaw decodes the response body once into raw output-item
// tokens, aligned with the output array. The walk passes the selected token to
// captureResponsesItemArgsRaw, so a malformed sibling item does not lose
// capture for well-formed function calls. It returns nil if the focused decode
// fails, so the caller falls back to the existing string-form args.
func captureResponsesArgsRaw(body []byte) []json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var focused struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := rawArgsBodyDecode(body, &focused); err != nil {
		return nil
	}
	return focused.Output
}

func captureResponsesItemArgsRaw(item []byte) (json.RawMessage, bool) {
	var focused struct {
		Type      string          `json:"type"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(item, &focused); err != nil || focused.Type != "function_call" {
		return nil, false
	}
	return focused.Arguments, true
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
