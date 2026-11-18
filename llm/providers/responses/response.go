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

type responsesTerminalCall struct {
	llm.ToolCallData
	terminalName      bool
	terminalArguments bool
}

func functionItemIDs(item map[string]any) (itemID, callID string) {
	itemID, _ = item["id"].(string)
	if itemID == "" {
		itemID, _ = item["item_id"].(string)
	}
	callID, _ = item["call_id"].(string)
	return itemID, callID
}

// Item-only fields can come from the stream or anywhere in the terminal
// array. They belong to a call only if the entire array identifies one owner.
func (acc *responsesOutputAccumulator) indexTerminalCalls(out []any) map[string]string {
	owners := make(map[string]string)
	for _, entry := range out {
		item, _ := entry.(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		itemID, callID := functionItemIDs(item)
		if itemID != "" && callID != "" {
			if prior, seen := owners[itemID]; seen && prior != callID {
				callID = ""
				if acc != nil {
					acc.byID[itemID] = nil
				}
			}
			owners[itemID] = callID
		}
	}
	return owners
}

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload or output_item.done events.
func responseContentFromOutputItems(out []any, body []byte, rawItems []json.RawMessage, stream *responsesOutputAccumulator) []llm.ContentPart {
	var rawBodyItems []json.RawMessage
	var rawBodyCaptured bool
	var content []llm.ContentPart
	var calls map[string]*responsesTerminalCall
	var owners map[string]string
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
			var substantive bool
			content, substantive = observed.appendMessage(content, item, false)
			if substantive {
				sparse = false
			}
		case "function_call":
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			itemID, callID := functionItemIDs(item)
			if owners == nil && itemID != "" && (callID == "" || stream != nil && stream.byID[itemID].provisionalCall()) {
				owners = stream.indexTerminalCalls(out)
			}
			if callID == "" && itemID != "" {
				owner, declared := owners[itemID]
				if declared && owner == "" {
					continue
				}
				if stream != nil {
					if prior, seen := stream.byID[itemID]; seen && (prior == nil || !prior.acceptsCallID(owner)) {
						continue
					}
				}
				callID = owner
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
			terminalName, terminalArguments := name != "", args != ""
			if stream != nil {
				if issued := stream.byCallID[callID]; issued != nil {
					itemID = stream.bindCallItem(issued, itemID)
				}
				if observed := stream.terminalItem("function_call", itemID, callID, itemIdx, out); observed != nil && observed.tool != nil {
					prior := observed.tool
					callID = cmp.Or(callID, prior.callID)
					itemID = cmp.Or(itemID, prior.itemID)
					name = cmp.Or(name, prior.name)
					if args == "" {
						args = prior.args.value()
					}
				}
			}
			if callID == "" {
				continue
			}
			if prior := calls[callID]; prior != nil {
				// Repeated references may complete one issued call at its first position.
				prior.ItemID = cmp.Or(prior.ItemID, itemID)
				if prior.Name == "" || (terminalName && !prior.terminalName) {
					prior.Name, prior.terminalName = name, terminalName
				}
				if len(prior.Arguments) == 0 || (terminalArguments && !prior.terminalArguments) {
					prior.Arguments = json.RawMessage(args)
					prior.terminalArguments = terminalArguments
				}
				continue
			}
			call := &responsesTerminalCall{
				ID:                callID,
				ItemID:            itemID,
				Name:              name,
				Arguments:         json.RawMessage(args),
				Type:              "function",
				terminalName:      terminalName,
				terminalArguments: terminalArguments,
			}
			if calls == nil {
				calls = make(map[string]*responsesTerminalCall)
			}
			calls[callID] = call
			content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &call.ToolCallData})
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
			if stream != nil {
				id, _ := item["id"].(string)
				observed = stream.terminalItem("reasoning", id, "", itemIdx, out)
			}
			thinking, native := responsesThinking(item, observed)
			if stream != nil {
				if native == reasoningSubstantive || (thinking != nil && thinking.EncryptedContent != "") {
					sparse = false
				}
				if observed != nil {
					thinking = observed.settleReasoning(item, thinking)
					observed.discardDisplayMirror(thinking)
				}
			}
			if thinking != nil {
				content = append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
			}
		default:
			// ignore
		}
		if sparse && (len(content) > start || typ == "message" && observed != nil) {
			// A selected message can clear its parts. Remember that empty result
			// so sparse recovery does not reconstruct it from earlier events.
			placeholders = append(placeholders, responsesTerminalItem{observed, content[start:len(content):len(content)]})
		}
	}
	if sparse {
		return stream.sparseContent(placeholders, terminalReasoning)
	}
	if stream != nil {
		var display []llm.ContentPart
		for _, item := range stream.output {
			display = item.appendDisplay(display)
		}
		if len(display) != 0 {
			return append(display, content...)
		}
	}
	return content
}

func (item *responsesOutputItem) discardDisplayMirror(thinking *llm.ThinkingData) {
	if thinking == nil || len(item.display) == 0 && len(item.summaries) == 0 {
		return
	}
	if !slices.ContainsFunc(thinking.ResponsesTextParts, func(p llm.ResponsesTextPart) bool { return p.End > p.Start }) {
		return
	}
	// Native content settles its provisional raw deltas. A summary is a
	// separate display source unless the same bytes already appear in the item.
	item.display = nil
	maps.DeleteFunc(item.summaries, func(_ int64, fragments responsesFragments) bool {
		text := fragments.value()
		return text == thinking.Text || slices.ContainsFunc(thinking.ResponsesTextParts, func(p llm.ResponsesTextPart) bool {
			return thinking.Text[p.Start:p.End] == text
		})
	})
}

func (item *responsesOutputItem) appendDisplay(content []llm.ContentPart) []llm.ContentPart {
	display := item.display
	if len(item.summaries) > 0 {
		display = slices.Clone(display)
		for _, fragments := range item.summaries {
			display = append(display, fragments...)
		}
		slices.SortFunc(display, func(a, b responsesFragment) int { return cmp.Compare(a.order, b.order) })
	}
	if text := display.value(); text != "" {
		return append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: text}})
	}
	return content
}

func (acc *responsesOutputAccumulator) terminalItem(kind, id, callID string, index int, out []any) *responsesOutputItem {
	compatible := func(item *responsesOutputItem) bool {
		return item.accepts(kind, id, -1) && item.acceptsCallID(callID)
	}
	named := acc.namedItem(kind, id)
	if !compatible(named) {
		named = nil
	}
	if called := acc.callItem(callID); compatible(called) {
		named = acc.join(named, called)
	}
	indexed := acc.byIndex[int64(index)]
	if !compatible(indexed) || indexed.ambiguousCallIndex {
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
			otherCallID, _ := item["call_id"].(string)
			if indexed != nil && ((indexed.id != "" && (item["id"] == indexed.id || item["item_id"] == indexed.id)) ||
				(otherCallID != "" && acc.callItem(otherCallID) == indexed)) {
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

// A terminal can omit native reasoning or supply a single placeholder. An
// explicit empty array clears the content. Other typed slots own their indexes.
func (item *responsesOutputItem) settleReasoning(terminalItem map[string]any, terminal *llm.ThinkingData) *llm.ThinkingData {
	if item.done == nil && len(item.text) == 0 {
		return terminal
	}
	if terminal == nil {
		terminal = &llm.ThinkingData{ID: item.reasoningID(terminalItem), Summary: item.reasoningSummary(terminalItem["summary"])}
	}
	parts, supplied := terminalItem["content"].([]any)
	if !supplied || singleReasoningPlaceholder(parts) {
		// The stream can supply several native slots even when their item
		// repeats only the types and their text arrived in indexed finals.
		prior, _ := responsesThinking(item.done, item)
		if prior != nil && slices.ContainsFunc(prior.ResponsesTextParts, func(p llm.ResponsesTextPart) bool { return p.Type == "reasoning_text" }) {
			terminal.Text, terminal.ResponsesTextParts = prior.Text, prior.ResponsesTextParts
		}
	}
	ciphertext, _ := item.done["encrypted_content"].(string)
	terminal.EncryptedContent = cmp.Or(terminal.EncryptedContent, ciphertext)
	if terminal.EncryptedContent == "" && len(terminal.ResponsesTextParts) == 0 {
		return nil
	}
	return terminal
}

// Empty terminals and placeholders admit observed answers and calls at their
// stream positions. A named reasoning placeholder admits only its own reasoning.
func (acc *responsesOutputAccumulator) sparseContent(terminal []responsesTerminalItem, terminalReasoning bool) []llm.ContentPart {
	matched := make(map[*responsesOutputItem][]llm.ContentPart, len(terminal))
	var selectedMessages []*responsesOutputItem
	var content []llm.ContentPart
	var previous *responsesOutputItem
	for _, item := range terminal {
		if item.observed != nil {
			previous = item.observed
			previous.displayOnly = false // The terminal supplies a native item at this position.
			if _, selected := matched[previous]; previous.kind == "message" && !selected {
				selectedMessages = append(selectedMessages, previous)
			}
			matched[previous] = item.parts
		} else if previous != nil {
			matched[previous] = append(matched[previous], item.parts...)
		} else {
			content = append(content, item.parts...)
		}
	}
	if !terminalReasoning {
		for _, item := range acc.output {
			if item.kind != "reasoning" {
				continue
			}
			// An item completion or a declared and finalized native part
			// supplies this output position before unindexed siblings settle.
			if thinking, _ := responsesThinking(item.done, item); thinking != nil {
				item.displayOnly = false
				item.discardDisplayMirror(thinking)
				matched[item] = []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: thinking}}
			}
		}
	}
	acc.orderOutput()
	for _, item := range acc.output {
		// Keep the slots of omitted stream items. The terminal controls the
		// relative order of the message slots it explicitly selected.
		if _, selected := matched[item]; item.kind == "message" && selected {
			item, selectedMessages = selectedMessages[0], selectedMessages[1:]
		}
		content = item.appendDisplay(content)
		if parts, ok := matched[item]; ok {
			content = append(content, parts...)
			continue
		}
		if item.kind == "reasoning" {
			continue
		}
		if item.kind == "message" {
			content, _ = item.appendMessage(content, item.done, true)
			continue
		}
		if item.done != nil {
			parts := responseContentFromOutputItems([]any{item.done}, nil, []json.RawMessage{item.raw}, nil)
			if len(parts) != 0 {
				content = append(content, parts...)
				continue
			}
		}
		if st := item.tool; st != nil && st.hasOutput && st.callID != "" {
			content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID: st.callID, ItemID: st.itemID, Name: st.name, Type: "function", Arguments: json.RawMessage(st.args.value()),
			}})
		}
	}
	return content
}

func responsesMessagePartText(kind string, part map[string]any) (string, bool) {
	switch kind {
	case "output_text":
		text, ok := part["text"].(string)
		return text, ok
	case "refusal":
		text, ok := part["refusal"].(string)
		return text, ok
	default:
		return "", false
	}
}

// The terminal or completed content array selects the message's slots. A
// selected slot may inherit its own text. Only a finalized answer or a refusal
// makes a message reference substantive enough to withdraw unselected calls.
func (item *responsesOutputItem) appendMessage(content []llm.ContentPart, message map[string]any, streamOnly bool) ([]llm.ContentPart, bool) {
	if item != nil {
		for _, update := range item.pendingMessage {
			item.applyMessageUpdate(update)
		}
		item.pendingMessage = nil
	}
	start := len(content)
	parts, terminalParts := message["content"].([]any)
	if !terminalParts && item != nil {
		parts = item.messageParts
	}
	var observed map[int64]*responsesOutputText
	if item != nil {
		observed = item.text
	}
	appendText := func(text string, refusal bool) {
		content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: text, ResponsesMessageStart: len(content) == start, ResponsesRefusal: refusal})
	}
	var substantive, terminalValue, recovered bool
	observedText := func(part *responsesOutputText, completedSlot bool) string {
		text := part.value()
		finalized := part.finalType == "output_text" || completedSlot
		substantive = substantive || finalized
		recovered = recovered || finalized || text != ""
		return text
	}
	for index, entry := range parts {
		p, _ := entry.(map[string]any)
		kind, _ := p["type"].(string)
		text, ok := responsesMessagePartText(kind, p)
		if kind == "refusal" {
			// The refusal selects this message even if its text is empty. An
			// earlier output_text or tool call cannot fill that selection.
			if part := observed[int64(index)]; !ok && part != nil && part.finalType == "refusal" {
				text = part.final
			}
			appendText(text, true)
			substantive, terminalValue = true, true
			continue
		}
		if kind != "output_text" {
			continue
		}
		if text != "" {
			substantive = true
			terminalValue = terminalValue || terminalParts
			recovered = recovered || !terminalParts
		} else {
			source := observed[int64(index)]
			if len(parts) == 1 && (source == nil || source.finalType == "" && len(source.deltas) == 0) {
				source = cmp.Or(observed[-1], source)
			}
			if source != nil && source.finalType != "refusal" {
				// A completed slot confirms its observed value, including an empty string.
				completed := typedPartAt(item.messageParts, int64(index), kind) != nil
				text, ok = observedText(source, completed), true
			}
		}
		if ok {
			appendText(text, false)
		}
	}
	if parts == nil {
		for _, index := range slices.Sorted(maps.Keys(observed)) {
			if index < 0 && len(observed) != 1 {
				continue
			}
			part := observed[index]
			if part.finalType == "refusal" {
				appendText(part.final, true)
				substantive, terminalValue = true, true
				continue
			}
			appendText(observedText(part, false), false)
		}
	}
	// Explicit terminal and completed phases can clear an announcement. Only
	// a message recovered from the stream can inherit an announcement alone.
	phaseValue, hasPhase := message["phase"]
	phase, _ := phaseValue.(string)
	if !hasPhase && item != nil && (item.phase.completed || streamOnly || recovered && !terminalValue) {
		phase = item.phase.value
	}
	if phase != "" {
		for i := start; i < len(content); i++ {
			content[i].Phase = phase
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

func singleReasoningPlaceholder(parts []any) bool {
	return len(parts) == 1 && typedPartAt(parts, 0, "reasoning_text")["text"] == ""
}

// A completed item owns its supplied text. A reasoning_text.done frame can
// supply the same typed slot when the item did not repeat its text.
func (item *responsesOutputItem) settledReasoningText(index int, kind string) (text string, present bool) {
	if item == nil || kind == "" {
		return "", false
	}
	parts, _ := item.done["content"].([]any)
	text, present = typedPartAt(parts, int64(index), kind)["text"].(string)
	if final := item.text[int64(index)]; !present && kind == "reasoning_text" && final != nil && final.reasoningFinal {
		return final.final, true
	}
	return text, present
}

func (item *responsesOutputItem) reasoningID(terminal map[string]any) string {
	if id, supplied := terminal["id"].(string); supplied {
		return id
	}
	if item != nil {
		if id, supplied := item.done["id"].(string); supplied {
			return id
		}
		if item.hasNativeID {
			return item.id
		}
	}
	return ""
}

func (item *responsesOutputItem) reasoningSummary(raw any) []string {
	selected, supplied := raw.([]any)
	if item == nil {
		return parseReasoningSummary(selected, nil)
	}
	completed, _ := item.done["summary"].([]any)
	if supplied {
		selected = completedReasoningParts(completed, selected)
	} else {
		selected = completed
	}
	if selected != nil {
		return parseReasoningSummary(selected, item.finalSummaries)
	}
	// Summary arrays have no index field. A missing slot cannot be filled
	// by shifting a later finalized part into its position.
	var summary []string
	for index := int64(0); ; index++ {
		final, ok := item.finalSummaries[index]
		if !ok {
			return summary
		}
		summary = append(summary, final.text)
	}
}

// Native bytes can consist entirely of whitespace. Such an item is kept in
// history while still allowing the stream to supply missing answer or tool items.
func responsesThinking(item map[string]any, prior *responsesOutputItem) (*llm.ThinkingData, reasoningContent) {
	encrypted, _ := item["encrypted_content"].(string)
	native := reasoningAbsent
	parts, supplied := item["content"].([]any)
	placeholder := singleReasoningPlaceholder(parts)
	var display strings.Builder
	var textParts []llm.ResponsesTextPart
	appendPart := func(kind, text string) {
		if len(textParts) != 0 {
			display.WriteString(reasoningPartSeparator)
		}
		start := display.Len()
		display.WriteString(text)
		textParts = append(textParts, llm.ResponsesTextPart{Type: kind, Start: start, End: display.Len()})
	}
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
		if p["text"] == nil || placeholder {
			if observed, present := prior.settledReasoningText(i, kind); present {
				text, ok = observed, true
			}
		}
		if !ok || (kind == "" && strings.TrimSpace(text) == "") {
			continue
		}
		appendPart(kind, text)
	}
	var announced bool
	if !supplied && prior != nil {
		if _, completed := prior.done["content"].([]any); !completed {
			for _, index := range slices.Sorted(maps.Keys(prior.text)) {
				part := prior.text[index]
				if part.reasoningType == "reasoning_text" && part.reasoningFinal {
					appendPart(part.reasoningType, part.final)
					announced = true
				}
			}
		}
	}
	text := display.String()
	if encrypted == "" && (len(textParts) == 0 || native == reasoningAbsent && !announced && strings.TrimSpace(text) == "") {
		return nil, native
	}
	return &llm.ThinkingData{
		ID:                 prior.reasoningID(item),
		Text:               text,
		EncryptedContent:   encrypted,
		Summary:            prior.reasoningSummary(item["summary"]),
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
	acc.settleContestedArguments(out)
	r := responseWithContent(raw, requestedModel, responseContentFromOutputItems(out, body, nil, acc))
	// The adapter has already filled recoverable placeholders. An explicit
	// selection can yield zero represented parts (for example, file_search_call)
	// and still withdraw an earlier streamed tool call.
	r.ContentAuthoritative = len(out) != 0
	if !r.ContentAuthoritative && len(r.Message.Content) == 0 && acc.completed != 0 {
		// A completed message can clear the generic accumulator's draft
		// even when the terminal omits the completed item.
		r.ContentAuthoritative = slices.ContainsFunc(acc.output, func(item *responsesOutputItem) bool {
			return item.messagePartsOrder != 0
		})
	}
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
