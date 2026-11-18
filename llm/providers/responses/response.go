package responses

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

type responsesTerminalItem struct {
	observed *responsesOutputItem
	parts    []llm.ContentPart
}

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload or output_item.done events.
func responseContentFromOutputItems(out []any, body []byte, rawItems []json.RawMessage, stream *responsesOutputAccumulator) []llm.ContentPart {
	var rawBodyItems []json.RawMessage
	var rawBodyCaptured bool
	var content []llm.ContentPart
	sparse := stream != nil
	var placeholders []responsesTerminalItem
	var terminalReasoning bool
	for itemIdx, itemAny := range out {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		if typ != "reasoning" && typ != "message" {
			sparse = false
		}
		start := len(content)
		var observed *responsesOutputItem
		switch typ {
		case "message":
			phase, _ := item["phase"].(string)
			var substantive bool
			content, substantive = appendResponsesText(content, item, phase)
			if substantive {
				sparse = false
			} else if stream != nil {
				id, _ := item["id"].(string)
				observed = stream.terminalMessage(id, itemIdx, out)
				if observed != nil && len(content) <= start+1 && (observed.textComplete || len(observed.text) > 0) {
					if phase == "" {
						phase = observed.phase
					}
					content = observed.appendMessage(content[:start], phase)
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
			terminalReasoning = true
			thinking, native := responsesThinking(item)
			if stream != nil {
				if native == reasoningSubstantive {
					sparse = false
				} else {
					id, _ := item["id"].(string)
					observed = stream.byID[id]
					if observed != nil && observed.kind == "reasoning" {
						thinking = observed.settleReasoning(item, thinking, native)
					} else {
						observed = nil
					}
				}
			}
			if thinking != nil {
				content = append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
			}
		default:
			// ignore
		}
		if sparse && len(content) > start {
			placeholders = append(placeholders, responsesTerminalItem{observed, content[start:len(content):len(content)]})
		}
	}
	if sparse {
		return stream.sparseContent(placeholders, terminalReasoning)
	}
	return content
}

func (acc *responsesOutputAccumulator) terminalMessage(id string, index int, out []any) *responsesOutputItem {
	if item := acc.byID[id]; item != nil && item.kind == "message" {
		return item
	}
	indexed := acc.byIndex[int64(index)]
	if indexed != nil && (indexed.kind != "message" || (id != "" && indexed.id != "" && indexed.id != id)) {
		indexed = nil
	}
	onlyMessage := true
	for i, entry := range out {
		item, _ := entry.(map[string]any)
		if i != index && item["type"] == "message" {
			onlyMessage = false
			if indexed != nil && indexed.id != "" && item["id"] == indexed.id {
				indexed = nil // Another terminal message explicitly owns this stream item.
			}
		}
	}
	if indexed != nil || !onlyMessage {
		return indexed
	}
	item, _ := acc.uniqueMessage(id, -1, false)
	return item
}

// Only a completed reasoning item with the same native ID may supply
// reasoning absent from the terminal.
func (output *responsesOutputItem) settleReasoning(item map[string]any, terminal *llm.ThinkingData, terminalNative reasoningContent) *llm.ThinkingData {
	prior, native := responsesThinking(output.done)
	if native == reasoningAbsent {
		return terminal
	}
	// Terminal whitespace is already native text. A zero-length part can
	// still be filled, and ciphertext is the preferred continuation.
	if prior.EncryptedContent == "" && (terminalNative == reasoningWhitespace || terminalNative == native) {
		return terminal
	}
	if summary := parseReasoningSummary(item["summary"]); summary != nil {
		prior.Summary = summary
	}
	return prior
}

// Empty terminals and placeholders admit observed answers and calls at their
// stream positions. A named reasoning placeholder admits only its own reasoning.
func (acc *responsesOutputAccumulator) sparseContent(terminal []responsesTerminalItem, terminalReasoning bool) []llm.ContentPart {
	matched := make(map[*responsesOutputItem][]llm.ContentPart, len(terminal))
	var content []llm.ContentPart
	var previous *responsesOutputItem
	for _, item := range terminal {
		if item.observed != nil {
			previous = item.observed
			matched[previous] = item.parts
		} else if previous != nil {
			matched[previous] = append(matched[previous], item.parts...)
		} else {
			content = append(content, item.parts...)
		}
	}
	acc.orderOutput()
	for _, item := range acc.output {
		if parts, ok := matched[item]; ok {
			content = append(content, parts...)
			continue
		}
		if terminalReasoning && item.kind == "reasoning" {
			continue
		}
		if item.kind == "message" {
			content = item.appendMessage(content, item.phase)
			continue
		}
		if item.done != nil {
			parts := responseContentFromOutputItems([]any{item.done}, nil, []json.RawMessage{item.raw}, nil)
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

// A completed message owns its parts. Its deltas fill a single empty
// placeholder or supply the message when no completed item arrived.
func (item *responsesOutputItem) appendMessage(content []llm.ContentPart, phase string) []llm.ContentPart {
	start := len(content)
	content, substantive := appendResponsesText(content, item.done, phase)
	if len(item.text) == 0 || len(content) > start+1 || substantive {
		return content
	}
	content = content[:start]
	for _, index := range slices.Sorted(maps.Keys(item.text)) {
		content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: item.text[index].String(), Phase: phase, ResponsesMessageStart: len(content) == start})
	}
	return content
}

func appendResponsesText(content []llm.ContentPart, message map[string]any, phase string) ([]llm.ContentPart, bool) {
	start := len(content)
	parts, _ := message["content"].([]any)
	var substantive bool
	for _, entry := range parts {
		p, _ := entry.(map[string]any)
		if text, ok := p["text"].(string); ok && p["type"] == "output_text" {
			content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: text, Phase: phase, ResponsesMessageStart: len(content) == start})
			substantive = substantive || text != ""
		}
	}
	return content, substantive
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

type reasoningContent uint8

const (
	reasoningAbsent reasoningContent = iota
	reasoningEmpty
	reasoningWhitespace
	reasoningSubstantive
)

// Native bytes can consist entirely of whitespace. Such an item is kept in
// history while still allowing the stream to supply missing answer or tool items.
func responsesThinking(item map[string]any) (*llm.ThinkingData, reasoningContent) {
	id, _ := item["id"].(string)
	encrypted, _ := item["encrypted_content"].(string)
	native := reasoningAbsent
	if encrypted != "" {
		native = reasoningSubstantive
	}
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
		if kind == "reasoning_text" && native < reasoningSubstantive {
			switch {
			case text == "":
				native = max(native, reasoningEmpty)
			case strings.TrimSpace(text) == "":
				native = reasoningWhitespace
			default:
				native = reasoningSubstantive
			}
		}
		if len(textParts) != 0 {
			display.WriteString(reasoningPartSeparator)
		}
		start := display.Len()
		display.WriteString(text)
		textParts = append(textParts, llm.ResponsesTextPart{Type: kind, Start: start, End: display.Len()})
	}
	text := display.String()
	if native == reasoningAbsent && strings.TrimSpace(text) == "" {
		return nil, native
	}
	return &llm.ThinkingData{
		ID:                 id,
		Text:               text,
		EncryptedContent:   encrypted,
		Summary:            parseReasoningSummary(item["summary"]),
		ResponsesTextParts: textParts,
	}, native
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
