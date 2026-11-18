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
			if stream != nil {
				id, _ := item["id"].(string)
				observed = stream.terminalItem("message", id, "", itemIdx, out)
			}
			phase, _ := item["phase"].(string)
			var substantive bool
			content, substantive = observed.appendMessage(content, item, phase)
			if substantive {
				sparse = false
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
			if stream != nil {
				if observed := stream.terminalItem("function_call", itemID, callID, itemIdx, out); observed != nil && observed.tool != nil {
					prior := observed.tool
					callID = cmp.Or(callID, prior.id)
					itemID = cmp.Or(itemID, prior.itemID)
					name = cmp.Or(name, prior.name)
					if args == "" {
						args = prior.args.value()
					}
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
			var prior map[string]any
			if stream != nil {
				id, _ := item["id"].(string)
				observed = stream.terminalItem("reasoning", id, "", itemIdx, out)
				if observed != nil {
					prior = observed.done
				}
			}
			thinking, native := responsesThinking(item, prior)
			if stream != nil {
				if native == reasoningSubstantive || (thinking != nil && thinking.EncryptedContent != "") {
					sparse = false
				}
				if observed != nil {
					thinking = observed.settleReasoning(item, thinking, native)
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

func (acc *responsesOutputAccumulator) terminalItem(kind, id, callID string, index int, out []any) *responsesOutputItem {
	compatible := func(item *responsesOutputItem) bool {
		return item.accepts(kind, id, -1) && (callID == "" || item.tool == nil || !item.tool.hasCallID || item.tool.id == callID)
	}
	named := acc.namedItem(kind, id)
	if !compatible(named) {
		named = nil
	}
	if called := acc.byCallID[callID]; compatible(called) {
		named = acc.join(named, called)
	}
	indexed := acc.byIndex[int64(index)]
	if !compatible(indexed) {
		indexed = nil
	}
	if indexed == named && (named != nil || kind != "message") {
		return named
	}
	onlyItem := true
	for i, entry := range out {
		item, _ := entry.(map[string]any)
		if i != index && item["type"] == kind {
			onlyItem = false
			if indexed != nil && ((indexed.id != "" && (item["id"] == indexed.id || item["item_id"] == indexed.id)) ||
				(indexed.tool != nil && item["call_id"] == indexed.tool.id)) {
				indexed = nil // Another terminal item explicitly owns this stream item.
			}
		}
	}
	if observed := acc.join(named, indexed); observed != nil || !onlyItem || kind != "message" {
		return observed
	}
	item, _ := acc.uniqueMessage(id, -1, false)
	return item
}

// A terminal without native reasoning or with a single explicit empty
// placeholder can use the whole completed item. Other typed slots own their
// indexes, including a slot whose text is missing in both sources.
func (item *responsesOutputItem) settleReasoning(terminalItem map[string]any, terminal *llm.ThinkingData, terminalNative reasoningContent) *llm.ThinkingData {
	if item.done == nil {
		return terminal
	}
	if terminal == nil {
		id, _ := terminalItem["id"].(string)
		terminal = &llm.ThinkingData{ID: id, Summary: parseReasoningSummary(terminalItem["summary"])}
	}
	parts, _ := terminalItem["content"].([]any)
	var placeholder bool
	if terminalNative == reasoningEmpty && len(parts) == 1 {
		part, _ := parts[0].(map[string]any)
		placeholder = part["text"] == ""
	}
	if terminalNative == reasoningAbsent || placeholder {
		if prior, native := responsesThinking(item.done, nil); prior != nil && native > terminalNative {
			terminal.Text, terminal.ResponsesTextParts = prior.Text, prior.ResponsesTextParts
		}
	}
	id, _ := item.done["id"].(string)
	ciphertext, _ := item.done["encrypted_content"].(string)
	terminal.ID = cmp.Or(terminal.ID, id)
	terminal.EncryptedContent = cmp.Or(terminal.EncryptedContent, ciphertext)
	if terminal.EncryptedContent == "" && len(terminal.ResponsesTextParts) == 0 {
		return nil
	}
	if terminal.Summary == nil {
		terminal.Summary = parseReasoningSummary(item.done["summary"])
	}
	return terminal
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
			previous.displayOnly = false // The terminal supplies a native item at this position.
			matched[previous] = item.parts
		} else if previous != nil {
			matched[previous] = append(matched[previous], item.parts...)
		} else {
			content = append(content, item.parts...)
		}
	}
	acc.orderOutput()
	for _, item := range acc.output {
		if len(item.display) != 0 {
			var hasText bool
			if m := matched[item]; len(m) != 0 && m[0].Thinking != nil {
				hasText = slices.ContainsFunc(m[0].Thinking.ResponsesTextParts, func(p llm.ResponsesTextPart) bool { return p.End > p.Start })
			} else if !terminalReasoning {
				parts, _ := item.done["content"].([]any)
				hasText = slices.ContainsFunc(parts, func(p any) bool {
					part, _ := p.(map[string]any)
					text, _ := part["text"].(string)
					return text != ""
				})
			}
			if !hasText {
				if display := item.display.value(); display != "" {
					content = append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: display}})
				}
			}
		}
		if parts, ok := matched[item]; ok {
			content = append(content, parts...)
			continue
		}
		if terminalReasoning && item.kind == "reasoning" {
			continue
		}
		if item.kind == "message" {
			content, _ = item.appendMessage(content, item.done, item.phase)
			continue
		}
		if item.done != nil {
			parts := responseContentFromOutputItems([]any{item.done}, nil, []json.RawMessage{item.raw}, nil)
			if len(parts) != 0 {
				content = append(content, parts...)
				continue
			}
		}
		if st := item.tool; st != nil && st.hasOutput && st.id != "" {
			content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID: st.id, ItemID: st.itemID, Name: st.name, Type: "function", Arguments: json.RawMessage(st.args.value()),
			}})
		}
	}
	return content
}

// Nonempty terminal text owns its index. An empty slot can be filled from
// the same observed part; unindexed text can fill only a single-part message.
func (item *responsesOutputItem) appendMessage(content []llm.ContentPart, message map[string]any, phase string) ([]llm.ContentPart, bool) {
	start := len(content)
	parts, _ := message["content"].([]any)
	var observed map[int64]*responsesOutputText
	if item != nil {
		observed = item.text
	}
	appendText := func(text string) {
		content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: text, Phase: phase, ResponsesMessageStart: len(content) == start})
	}
	var substantive, recovered bool
	for index, entry := range parts {
		p, _ := entry.(map[string]any)
		if p["type"] != "output_text" {
			continue
		}
		text, ok := p["text"].(string)
		if text != "" {
			substantive = true
		} else {
			source := observed[int64(index)]
			if len(parts) == 1 && (source == nil || source.value() == "") {
				source = observed[-1]
			}
			if source != nil {
				text, ok = source.value(), true
				recovered = recovered || text != ""
			}
		}
		if ok {
			appendText(text)
		}
	}
	if !substantive && len(parts) <= 1 {
		for _, index := range slices.Sorted(maps.Keys(observed)) {
			onlyUnindexed := index == -1 && len(parts) == 0 && len(observed) == 1
			if index >= int64(len(parts)) || onlyUnindexed {
				text := observed[index].value()
				recovered = recovered || text != ""
				appendText(text)
			}
		}
	}
	if recovered && !substantive && phase == "" {
		for i := start; i < len(content); i++ {
			content[i].Phase = item.phase
		}
	}
	return content, substantive
}

// Fill unclaimed native output positions by first appearance. Display-only
// records keep their observed places in the transcript and own no native slots.
func (acc *responsesOutputAccumulator) orderOutput() {
	if len(acc.byIndex) == 0 {
		return
	}
	var next int64
	native := make([]*responsesOutputItem, 0, len(acc.output))
	for _, item := range acc.output {
		if item.displayOnly {
			continue
		}
		native = append(native, item)
		if item.index < 0 {
			for acc.byIndex[next] != nil && !acc.byIndex[next].displayOnly {
				next++
			}
			item.index = next
			next++
		}
	}
	slices.SortStableFunc(native, func(a, b *responsesOutputItem) int { return cmp.Compare(a.index, b.index) })
	for i, item := range acc.output {
		if !item.displayOnly {
			acc.output[i], native = native[0], native[1:]
		}
	}
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
func responsesThinking(item, prior map[string]any) (*llm.ThinkingData, reasoningContent) {
	id, _ := item["id"].(string)
	encrypted, _ := item["encrypted_content"].(string)
	native := reasoningAbsent
	parts, _ := item["content"].([]any)
	priorParts, _ := prior["content"].([]any)
	var display strings.Builder
	var textParts []llm.ResponsesTextPart
	for i, part := range parts {
		p, _ := part.(map[string]any)
		text, ok := p["text"].(string)
		kind, _ := p["type"].(string)
		// Only the terminal's own bytes determine whether it selects output
		// membership. Recovering a part does not withdraw sparse siblings.
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
		if text == "" && (ok || p["text"] == nil) && kind != "" && i < len(priorParts) {
			source, _ := priorParts[i].(map[string]any)
			if source["type"] == kind {
				if observed, present := source["text"].(string); present {
					text, ok = observed, true
				}
			}
		}
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
	text := display.String()
	if encrypted == "" && (len(textParts) == 0 || native == reasoningAbsent && strings.TrimSpace(text) == "") {
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
