package responses

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

func responsesFunctionCallUnfinished(item map[string]any) bool {
	kind, _ := item["type"].(string)
	status, _ := item["status"].(string)
	return kind == "function_call" && (status == "incomplete" || status == "in_progress")
}

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload's "output" field or from
// decodeStream's accumulated response.output_item.done events, so both
// callers share this one walk.
func responseContentFromOutputItems(out []any, body []byte, responseIncomplete bool) []llm.ContentPart {
	return responseContentFromOutputItemsRaw(out, body, nil, true, responseIncomplete)
}

func responseContentFromOutputItemsRaw(out []any, body []byte, rawItems []json.RawMessage, coalesceCalls, responseIncomplete bool) []llm.ContentPart {
	var rawBodyItems []json.RawMessage
	var rawBodyCaptured bool
	var content []llm.ContentPart
	var callsByID map[string]*llm.ToolCallData
	for itemIdx, itemAny := range out {
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
			if responsesFunctionCallUnfinished(item) {
				continue
			}
			name, _ := item["name"].(string)
			callID, _ := item["call_id"].(string)
			itemID, _ := item["id"].(string)
			if itemID == "" {
				itemID, _ = item["item_id"].(string)
			}
			call := callsByID[callID]
			if call != nil {
				// Later references complete missing fields at the first position.
				call.ItemID = cmp.Or(call.ItemID, itemID)
				call.Name = cmp.Or(call.Name, name)
				if len(call.Arguments) != 0 {
					continue
				}
			} else {
				call = &llm.ToolCallData{ID: callID, ItemID: itemID, Name: name, Type: "function"}
				if coalesceCalls && callID != "" {
					if callsByID == nil {
						callsByID = make(map[string]*llm.ToolCallData)
					}
					callsByID[callID] = call
				}
				content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: call})
			}
			// Decode the raw body once; index this item's token (rawItems
			// entries win when present).
			args, _ := item["arguments"].(string)
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
			call.Arguments, call.ArgumentsSpelling = llm.NormalizeToolArguments([]byte(args))
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
			status, _ := item["status"].(string)
			incomplete := status == "incomplete" || status == "in_progress" || (responseIncomplete && status != "completed")
			// OpenAI returns reasoning as encrypted_content plus summaries;
			// gateways that expose the raw thinking put reasoning_text parts
			// in content instead. Both ride back once the item finished (#6709).
			text := strings.Join(parseReasoningSummary(item["content"]), reasoningPartSeparator)
			contentParts := responsesReasoningParts(item["content"])
			summaryParts := responsesReasoningParts(item["summary"])
			if encryptedContent != "" || text != "" || len(contentParts) > 0 || len(summaryParts) > 0 {
				content = append(content, llm.ContentPart{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						ID:                    id,
						Text:                  text,
						EncryptedContent:      encryptedContent,
						Summary:               parseReasoningSummary(item["summary"]),
						ResponsesContentParts: contentParts,
						ResponsesSummaryParts: summaryParts,
						ResponsesIncomplete:   incomplete,
					},
				})
			}
		default:
			// ignore
		}
	}
	return content
}

func retainFallbackReasoning(r *llm.Response) {
	if !slices.ContainsFunc(r.Message.Content, func(p llm.ContentPart) bool {
		return p.Kind != llm.ContentThinking || p.Thinking == nil || p.Thinking.Text != "" || p.Thinking.EncryptedContent != ""
	}) {
		r.FallbackContent, r.Message.Content = r.Message.Content, nil
	}
}

func responsesReasoningParts(raw any) []llm.ResponsesReasoningPart {
	items, _ := raw.([]any)
	var parts []llm.ResponsesReasoningPart
	for _, item := range items {
		part, _ := item.(map[string]any)
		kind, _ := part["type"].(string)
		text, ok := part["text"].(string)
		if kind != "" && ok {
			parts = append(parts, llm.ResponsesReasoningPart{Type: kind, Text: text})
		}
	}
	return parts
}

// responsesAnswered reports whether the terminal settles an answer of its own: text, a refusal or a
// finished call. Reasoning, a blob and an empty part are not answers, so recovery still runs.
func responsesAnswered(terminal []any) bool {
	return slices.ContainsFunc(terminal, func(rawItem any) bool {
		item, _ := rawItem.(map[string]any)
		if kind, _ := item["type"].(string); kind == "function_call" {
			return !responsesFunctionCallUnfinished(item)
		} else if kind != "message" {
			return false
		}
		content, _ := item["content"].([]any)
		return slices.ContainsFunc(content, func(cAny any) bool {
			c, _ := cAny.(map[string]any)
			refusal, _ := c["refusal"].(string) // a wire refusal carries its text here, not in "text"
			text, _ := c["text"].(string)
			return refusal != "" || text != ""
		})
	})
}

// responsesTerminalDisowned reports whether the terminal settles this call as unfinished. Two records
// that both carry a call_id name the same call only when those ids match, so a shared item ID never
// disowns a call the terminal left alone. Without one of the call_ids, the item ID decides.
func responsesTerminalDisowned(terminal []any, id, callID string) bool {
	return slices.ContainsFunc(terminal, func(rawItem any) bool {
		item, _ := rawItem.(map[string]any)
		if !responsesFunctionCallUnfinished(item) {
			return false
		}
		theirCallID, _ := item["call_id"].(string)
		if callID != "" && theirCallID != "" {
			return callID == theirCallID
		}
		theirID, _ := item["id"].(string)
		return id != "" && id == theirID
	})
}

// adoptTerminalPhases gives each streamed message the phase its own terminal placeholder announced.
// Explicit item-ID matches reserve their placeholders first, and the message ordinal pairs the rest
// (when neither side names a conflicting ID and that slot is still free), so the first placeholder's
// phase never lands on every recovered message. Unmatched terminal phased messages and web_search_call
// items are appended directly so stream() converts them once.
func adoptTerminalPhases(streamed []any, streamedRaw []json.RawMessage, terminal []any) ([]any, []json.RawMessage) {
	messageItems := func(items []any) []map[string]any {
		var out []map[string]any
		for _, raw := range items {
			if item, _ := raw.(map[string]any); item != nil {
				if kind, _ := item["type"].(string); kind == "message" {
					out = append(out, item)
				}
			}
		}
		return out
	}
	phased := messageItems(terminal)   // terminal message items, in order
	messages := messageItems(streamed) // streamed message items, in message order
	matched := make([]bool, len(phased))
	adopt := func(item, placeholder map[string]any) {
		if p, _ := item["phase"].(string); p != "" {
			return
		}
		if phase, _ := placeholder["phase"].(string); phase != "" {
			item["phase"] = phase
		}
	}
	// A message that names a terminal placeholder by item ID reserves it first, so the
	// ordinal fallback can never hand that placeholder's phase to another message.
	reserved := make([]bool, len(messages))
	for i, item := range messages {
		id, _ := item["id"].(string)
		if id == "" {
			continue
		}
		if match := slices.IndexFunc(phased, func(m map[string]any) bool { return m["id"] == id }); match >= 0 {
			matched[match], reserved[i] = true, true
			adopt(item, phased[match])
		}
	}
	// A still-unmatched message pairs with the placeholder at its own ordinal, but only
	// when neither side names a conflicting ID and no ID already reserved that slot.
	for k, item := range messages {
		if reserved[k] || k >= len(phased) || matched[k] {
			continue
		}
		id, _ := item["id"].(string)
		theirID, _ := phased[k]["id"].(string)
		if id == "" || theirID == "" {
			matched[k] = true
			adopt(item, phased[k])
		}
	}
	for i, m := range phased {
		if !matched[i] {
			if phase, _ := m["phase"].(string); phase != "" {
				streamed = append(streamed, m)
				streamedRaw = append(streamedRaw, nil)
			}
		}
	}
	for _, raw := range terminal {
		item, _ := raw.(map[string]any)
		if kind, _ := item["type"].(string); kind != "web_search_call" {
			continue
		}
		tid, _ := item["id"].(string)
		duplicate := slices.ContainsFunc(streamed, func(sRaw any) bool {
			sItem, _ := sRaw.(map[string]any)
			if sk, _ := sItem["type"].(string); sk != "web_search_call" {
				return false
			}
			sid, _ := sItem["id"].(string)
			if tid != "" && sid != "" {
				return tid == sid
			}
			return reflect.DeepEqual(sItem, item)
		})
		if !duplicate {
			streamed = append(streamed, item)
			streamedRaw = append(streamedRaw, nil)
		}
	}
	return streamed, streamedRaw
}

// settleResponsesTerminalOutput decides the settled message content and finish reason for a
// Responses-API terminal payload. The terminal is authoritative: its text, refusal or finished call
// wins. When it settles no answer of its own, the stream's output_item.done events are recovered in
// its place, minus any call the terminal marks unfinished. A filtered answer is never recovered,
// because the provider retracted it. Shared by the live decoder and offline recomputation.
func settleResponsesTerminalOutput(r *llm.Response, rawResp map[string]any, acc *responsesOutputAccumulator) {
	// A terminal call that supplied no arguments is settled with the stream's own
	// bytes before the live decoder announces it and before recorded replay
	// returns it. A filtered terminal retracted its answer, so it borrows nothing.
	if r.Finish.Reason != llm.FinishReasonContentFilter {
		acc.fillTerminalArguments(r)
	}
	terminalOutput, _ := rawResp["output"].([]any)
	inc, recovered := rawResp["status"] == "incomplete", false // an incomplete response settles no finish reason
	r.OutputSettled = true                                     // settles here, so a draft call never reaches ToolCalls
	switch {
	// A filtered answer was retracted, so never recover the stream; the terminal's own stays.
	case r.Finish.Reason != llm.FinishReasonContentFilter && !responsesAnswered(terminalOutput) && acc != nil && len(acc.entries) > 0:
		// The terminal settles no answer of its own, so the stream's events are recovered in its
		// place, minus any call the terminal marks unfinished. Only an incomplete response may disown
		// a call: a settled response keeps every call the stream completed, so Finish stays tool_calls.
		accumulatedOutput, accumulatedRawOutput := acc.recoveredOutput(terminalOutput)
		if inc {
			// A recovered call named only by item ID carries that ID as its call_id, so
			// pass the issued id (empty there) and let the item ID decide. A provider
			// call_id is still compared as a call_id, keeping distinct calls apart.
			for i := range acc.entries {
				e := &acc.entries[i]
				if e.item["type"] != "function_call" {
					continue
				}
				id, _ := e.item["id"].(string)
				if responsesTerminalDisowned(terminalOutput, id, e.issuedCallID) {
					e.item["status"] = "incomplete"
				}
			}
		}
		// Streamed reasoning resolves completion in recoveredOutput; terminal-only status-less items
		// already inherited inc from fromResponses.
		accumulatedOutput, accumulatedRawOutput = adoptTerminalPhases(accumulatedOutput, accumulatedRawOutput, terminalOutput)
		content := responseContentFromOutputItemsRaw(accumulatedOutput, nil, accumulatedRawOutput, false, false)
		for _, p := range content { // a summary displays as thought text, its typed parts as summary
			if th := p.Thinking; th != nil && th.Text == "" && len(th.Summary) > 0 {
				th.Text = strings.Join(th.Summary, reasoningPartSeparator)
			}
		}
		terminal := append(slices.Clone(r.FallbackContent), r.Message.Content...)
		r.Message.Content, r.FallbackContent, recovered = llm.MergeFallbackReasoning(content, terminal), nil, true
	case len(terminalOutput) > 0:
		settledItems := acc.settledCount()
		if settledItems == 0 {
			return
		}
		// The stream accumulator holds only settled items.
		count := len(terminalOutput)
		for _, rawItem := range terminalOutput {
			item, _ := rawItem.(map[string]any)
			if responsesFunctionCallUnfinished(item) {
				count--
			}
		}
		if count == settledItems {
			return
		}
		r.Warnings = append(r.Warnings, llm.Warning{
			Code:    "responses_output_item_count_mismatch",
			Message: fmt.Sprintf("terminal output items=%d differ from accumulated stream items=%d", count, settledItems),
		})
	}
	if recovered && !inc {
		r.Finish = llm.FinishReason{Reason: map[bool]string{true: "stop", false: "tool_calls"}[len(r.ToolCalls()) == 0]}
	}
}

// fillTerminalArguments gives a terminal call the bytes the stream accumulated
// when the terminal supplied none, matched by the call's explicit ID or, only
// when that state learned no different call ID, by the item's state. It runs
// before recovered output is merged in, so a recovered completion keeps the
// bytes its own record carried.
func (acc *responsesOutputAccumulator) fillTerminalArguments(r *llm.Response) {
	if acc == nil || r == nil {
		return
	}
	for i := range r.Message.Content {
		part := &r.Message.Content[i]
		tc := part.ToolCall
		if part.Kind != llm.ContentToolCall || tc == nil || tc.ID == "" || len(tc.Arguments) > 0 {
			continue
		}
		st := acc.lookupState(tc.ID, tc.ItemID)
		if st == nil || st.args.Len() == 0 {
			continue
		}
		tc.Arguments, tc.ArgumentsSpelling = llm.NormalizeToolArguments(st.args.Bytes())
	}
}

// fromResponses maps a Responses API JSON object into an llm.Response.
// Provider is left as "openai": the non-streaming caller (complete.go, via
// protocolhttp.Do) stamps it to res.Instance after this returns, and the
// streaming caller (stream.go's decodeStream) stamps it itself since it
// calls this directly, outside Do.
//
// body is the raw response body bytes for byte-faithful argument capture;
// nil falls back to the existing string-form path.
func fromResponses(raw map[string]any, requestedModel string, body []byte) llm.Response {
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
		msg.Content = responseContentFromOutputItems(out, body, raw["status"] == "incomplete")
	}

	r.Message = msg
	retainFallbackReasoning(&r)

	// Check Responses API status/incomplete_details for finish reason.
	status, _ := raw["status"].(string)
	switch {
	case status == "incomplete":
		r.OutputSettled = true
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
