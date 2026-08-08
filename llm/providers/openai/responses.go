package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/invariant"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
	"primeradiant.com/serf/llm/providers/internal/transport"
)

func (a *Adapter) buildRequestBody(req llm.Request) (map[string]any, error) {
	instructions, inputItems, err := toResponsesInput(req.Messages, req.Model)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":               a.wireModel(req.Model),
		"instructions":        instructions,
		"input":               inputItems,
		"parallel_tool_calls": true,
		"store":               false,
	}

	// The codex backend serves gpt-5.6 through its "responses-lite" variant,
	// which takes a restructured request (mirroring the codex CLI,
	// codex-rs/core/src/client.rs build_responses_request): instructions and
	// tools ride inside the input, and parallel tool calls are disabled.
	codexLite := a.usesCodexBackend() && responsesLiteModel(req.Model)

	var tools []map[string]any
	if len(req.Tools) > 0 {
		tools = toResponsesTools(req.Tools)
	}
	if req.WebSearch {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	if codexLite {
		// Tools become a developer additional_tools input item (always
		// present, even empty), and base instructions become a developer
		// message after it; the top-level fields are emptied.
		toolsAny := make([]any, 0, len(tools))
		for _, tool := range tools {
			toolsAny = append(toolsAny, tool)
		}
		prefix := []any{map[string]any{
			"type":  "additional_tools",
			"role":  "developer",
			"tools": toolsAny,
		}}
		if instructions != "" {
			prefix = append(prefix, map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{
					map[string]any{"type": "input_text", "text": instructions},
				},
			})
		}
		body["input"] = append(prefix, inputItems...)
		body["instructions"] = ""
		body["parallel_tool_calls"] = false
	} else if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.ToolChoice != nil {
		tc, err := toResponsesToolChoice(*req.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = tc
	}
	if req.Temperature != nil && !a.usesCodexBackend() {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil && !a.usesCodexBackend() {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil && !a.usesCodexBackend() {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 && !a.usesCodexBackend() {
		body["stop"] = req.StopSequences
	}
	if strings.TrimSpace(req.PromptCacheKey) != "" {
		body["prompt_cache_key"] = strings.TrimSpace(req.PromptCacheKey)
	}
	if strings.TrimSpace(req.PreviousResponseID) != "" && !a.usesCodexBackend() {
		body["previous_response_id"] = strings.TrimSpace(req.PreviousResponseID)
	}
	if strings.TrimSpace(req.ConversationID) != "" && !a.usesCodexBackend() {
		body["conversation"] = strings.TrimSpace(req.ConversationID)
	}
	if strings.TrimSpace(req.ServiceTier) != "" && !a.usesCodexBackend() {
		body["service_tier"] = strings.TrimSpace(req.ServiceTier)
	}
	if strings.TrimSpace(req.SafetyIdentifier) != "" && !a.usesCodexBackend() {
		body["safety_identifier"] = strings.TrimSpace(req.SafetyIdentifier)
	}
	if ret := strings.TrimSpace(req.PromptCacheRetention); ret != "" && !a.usesCodexBackend() {
		// PromptCacheRetention is a legacy maximum-retention policy. GPT-5.6's
		// prompt_cache_options.ttl is a minimum lifetime with a 30m default, so
		// do not translate the legacy 24h value into the new field.
		if !responsesLiteModel(req.Model) {
			body["prompt_cache_retention"] = ret
		}
	}
	if strings.TrimSpace(req.Truncation) != "" && !a.usesCodexBackend() {
		body["truncation"] = strings.TrimSpace(req.Truncation)
	}
	if req.MaxToolCalls != nil && !a.usesCodexBackend() {
		body["max_tool_calls"] = *req.MaxToolCalls
	}
	if req.Background != nil && !a.usesCodexBackend() {
		body["background"] = *req.Background
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if a.usesCodexBackend() {
		clientMetadata := mergeStringMaps(req.Metadata, req.ClientMetadata)
		if len(clientMetadata) > 0 {
			body["client_metadata"] = clientMetadata
		}
	} else if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{
			"effort":  *req.ReasoningEffort,
			"summary": reasoningSummaryLevel(req.Model),
		}
	} else if responsesLiteModel(req.Model) {
		// Responses-lite models always reason; the codex client sends the
		// reasoning object on every request. Request summaries so thinking
		// displays, and let the API pick its default effort.
		body["reasoning"] = map[string]any{
			"summary": reasoningSummaryLevel(req.Model),
		}
	}
	if codexLite {
		// Responses-lite reasoning must span every turn, matching the codex
		// client's ReasoningContext::AllTurns.
		reasoning := body["reasoning"].(map[string]any)
		reasoning["context"] = "all_turns"
		body["reasoning"] = reasoning
	}
	include := append([]string{}, req.Include...)
	if req.ReasoningEffort != nil || responsesLiteModel(req.Model) {
		include = appendUniqueString(include, encryptedReasoning)
	}
	if len(include) > 0 {
		body["include"] = include
	}
	if req.ResponseFormat != nil {
		if rf := toResponsesResponseFormat(*req.ResponseFormat); rf != nil {
			text, _ := body["text"].(map[string]any)
			if text == nil {
				text = map[string]any{}
			}
			text["format"] = rf
			body["text"] = text
		}
	}
	if codexLite {
		// Responses-lite requests always carry text.verbosity; the codex
		// client's default for every gpt-5.6 variant is "low".
		text, _ := body["text"].(map[string]any)
		if text == nil {
			text = map[string]any{}
		}
		if _, ok := text["verbosity"]; !ok {
			text["verbosity"] = "low"
		}
		body["text"] = text
	}
	if req.ProviderOptions != nil {
		if ov, ok := req.ProviderOptions["openai"].(map[string]any); ok {
			for k, v := range ov {
				if codexLite && k == "parallel_tool_calls" {
					continue
				}
				if a.usesCodexBackend() && openAICodexUnsupportedRequestField(k) {
					continue
				}
				body[k] = v
			}
		}
	}
	return body, nil
}

func openAICodexUnsupportedRequestField(key string) bool {
	switch key {
	case "temperature",
		"top_p",
		"max_output_tokens",
		"stop",
		"metadata",
		"service_tier",
		"safety_identifier",
		"prompt_cache_retention",
		"prompt_cache_options",
		"truncation",
		"max_tool_calls",
		"background":
		return true
	default:
		return false
	}
}

// streamResponses is the raw Responses API streaming path.
// It emits errEmptyResponsesStream if the stream closes 200 OK with zero content.
func (a *Adapter) streamResponses(ctx context.Context, req llm.Request) (llm.Stream, error) {
	parentCtx := ctx
	sctx, cancel := context.WithCancel(ctx)
	sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, req.AdapterTimeout, true)
	defer timeoutCancel()

	body, err := a.buildRequestBody(req)
	if err != nil {
		cancel()
		return nil, err
	}
	body["stream"] = true

	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodPost, a.responsesURL(), bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, err
	}
	a.setRequestHeaders(httpReq, req)

	client := llm.ClientWithAdapterTimeout(a.Client, req.AdapterTimeout)
	resp, attempt, err := transport.DoWithAPIAttempts(parentCtx, client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.apiLogProviderInstance(),
			RequestModel:       req.Model,
			HistoryMode:        req.HistoryMode,
			EndpointFamily:     string(a.responsesEndpointFamily()),
			RequestBody:        requestBody,
			CredentialMaterial: a.apiLogCredentialMaterial(wireRequest),
		}
	})
	if err != nil {
		timeoutSource := llm.APITimeoutSourceForTransport(parentCtx, sctx, err)
		returnedErr := llm.WrapContextError("openai", err)
		attempt.Complete(llm.APIAttemptResult{Err: returnedErr}, timeoutSource, nil, err)
		cancel()
		return nil, returnedErr
	}

	// Handle non-2xx immediately.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		rawBytes, readErr := io.ReadAll(resp.Body)
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(rawBytes))
		dec.UseNumber()
		jsonErr := dec.Decode(&raw)
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := llm.ProviderFailureMessage("responses.create(stream)", rawBytes)
		returnedErr := llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
		decodeErr := jsonErr
		if readErr != nil {
			decodeErr = readErr
		}
		attempt.Complete(llm.APIAttemptResult{
			StatusCode:   resp.StatusCode,
			ResponseBody: rawBytes,
			Err:          returnedErr,
		}, llm.APITimeoutNone, decodeErr, nil)
		cancel()
		return nil, returnedErr
	}

	s := llm.NewChanStream(cancel)
	// STREAM_START
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})

	go a.decodeResponsesStream(sctx, cancel, resp, s, req, b, attempt)

	return s, nil
}

// responsesToolState accumulates one function-call tool call's identity and
// streamed arguments, addressable by either call_id or item_id (the wire
// shape's later events may reference either -- see
// responsesOutputAccumulator's itemToCallID). started tracks whether a
// tool-call-start stream event has been emitted for it yet; only the live
// decoder reads this field, but it's stored here because it's per-call
// state that already lives on this struct.
type responsesToolState struct {
	id, itemID, name string
	started          bool
	args             strings.Builder
}

// responsesOutputAccumulator tracks response.output_item.added/done and
// response.function_call_arguments.delta/done events to reconstruct the
// output array a response.completed payload should have carried, for
// sessions where the terminal payload's own "output" arrives empty (see
// settleResponsesTerminalOutput). It is the single implementation of that
// accumulation state machine -- including its tool-state lookups keyed by
// either call_id or item_id -- shared by the live streaming decoder
// (decodeResponsesStream) and offline recomputation
// (extractResponsesFromSSE in responses_recompute.go), so the two can't
// silently drift apart.
type responsesOutputAccumulator struct {
	toolStates   map[string]*responsesToolState
	itemToCallID map[string]string
	output       []any
}

func newResponsesOutputAccumulator() *responsesOutputAccumulator {
	return &responsesOutputAccumulator{
		toolStates:   map[string]*responsesToolState{},
		itemToCallID: map[string]string{},
	}
}

// Output returns the accumulated output array, in the wire shape a
// response.completed payload's "output" field carries.
func (acc *responsesOutputAccumulator) Output() []any { return acc.output }

// HandleOutputItemAdded pre-registers a function_call tool state from a
// response.output_item.added event's item, keyed by whichever of
// call_id/item_id are present. Non-function_call items are a no-op (the
// wire shape carries no other item type worth pre-registering here).
func (acc *responsesOutputAccumulator) HandleOutputItemAdded(item map[string]any) {
	if itemType, _ := item["type"].(string); itemType != "function_call" {
		return
	}
	callID, _ := item["call_id"].(string)
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
	stateID := callID
	if stateID == "" {
		stateID = itemID
	}
	if stateID == "" {
		return
	}
	st := acc.toolStates[stateID]
	if st == nil {
		st = &responsesToolState{id: stateID}
	}
	if callID != "" {
		st.id = callID
		acc.toolStates[callID] = st
	}
	if itemID != "" {
		st.itemID = itemID
		acc.itemToCallID[itemID] = st.id
		acc.toolStates[itemID] = st
	}
	if st.name == "" && name != "" {
		st.name = name
	}
	acc.toolStates[stateID] = st
}

// HandleFunctionCallArgumentsDelta merges one
// response.function_call_arguments.delta event's argument fragment into
// tool state, resolving call_id via item_id when call_id is absent. ok is
// false when the event can't be mapped to a tool call at all -- the caller
// should pass the raw payload through unmodified in that case.
func (acc *responsesOutputAccumulator) HandleFunctionCallArgumentsDelta(payload map[string]any) (state *responsesToolState, delta string, ok bool) {
	delta, _ = payload["delta"].(string)
	if delta == "" {
		delta, _ = payload["arguments"].(string)
	}
	callID, _ := payload["call_id"].(string)
	itemID, _ := payload["item_id"].(string)
	if callID == "" && itemID != "" {
		callID = acc.itemToCallID[itemID]
	}
	if callID == "" {
		callID = itemID
	}
	if callID == "" {
		callID, _ = payload["id"].(string)
	}
	name, _ := payload["name"].(string)
	if callID == "" || (delta == "" && name == "") {
		return nil, "", false
	}
	st := acc.toolStates[callID]
	if st == nil && itemID != "" {
		st = acc.toolStates[itemID]
		if st != nil {
			callID = st.id
		}
	}
	if st == nil {
		st = &responsesToolState{id: callID, name: name}
		acc.toolStates[callID] = st
	}
	if st.name == "" && name != "" {
		st.name = name
	}
	if delta != "" {
		st.args.WriteString(delta)
	}
	return st, delta, true
}

// HandleFunctionCallArgumentsDone applies a
// response.function_call_arguments.done event's authoritative full-arguments
// string, overriding whatever deltas were accumulated so far, resolving
// call_id via item_id when needed. ok is false when the event carried no
// resolvable call_id -- the caller should pass the raw payload through.
func (acc *responsesOutputAccumulator) HandleFunctionCallArgumentsDone(payload map[string]any) (state *responsesToolState, ok bool) {
	argsStr, _ := payload["arguments"].(string)
	callID, _ := payload["call_id"].(string)
	itemID, _ := payload["item_id"].(string)
	if callID == "" && itemID != "" {
		callID = acc.itemToCallID[itemID]
	}
	if callID == "" {
		callID = itemID
	}
	if callID == "" {
		return nil, false
	}
	st := acc.toolStates[callID]
	if st == nil && itemID != "" {
		st = acc.toolStates[itemID]
		if st != nil {
			callID = st.id
		}
	}
	if st == nil {
		st = &responsesToolState{id: callID}
		acc.toolStates[callID] = st
	}
	if itemID != "" {
		st.itemID = itemID
		acc.itemToCallID[itemID] = st.id
	}
	if argsStr != "" {
		st.args.Reset()
		st.args.WriteString(argsStr)
	}
	return st, true
}

// HandleOutputItemDone processes a response.output_item.done event's item,
// appending it to the accumulated output (mirroring the terminal payload's
// "output" array shape). For function_call items it also resolves/updates
// tool state, using the item's own "arguments" when present or falling back
// to whatever deltas were accumulated.
//
// It returns the raw item (nil if the payload carried none -- e.g. the
// event was malformed), and, only for a function_call item whose call_id
// resolved, the tool state and its full arguments string with ok=true.
// ok=false covers both "not a function_call" (rawItem is non-nil; already
// appended to Output()) and "function_call with no resolvable call_id"
// (rawItem is non-nil, not appended) -- callers distinguish the two by
// checking rawItem's own "type" field, matching the two different
// live-stream fallbacks (best-effort end-of-text vs. raw passthrough).
func (acc *responsesOutputAccumulator) HandleOutputItemDone(payload map[string]any) (rawItem map[string]any, state *responsesToolState, argsStr string, ok bool) {
	itemAny := payload["item"]
	if itemAny == nil {
		itemAny = payload["output_item"]
	}
	item, mapOK := itemAny.(map[string]any)
	if !mapOK {
		return nil, nil, "", false
	}
	it, _ := item["type"].(string)
	if it != "function_call" {
		acc.output = append(acc.output, item)
		return item, nil, "", false
	}
	callID, _ := item["call_id"].(string)
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
	argsStr, _ = item["arguments"].(string)
	if callID == "" && itemID != "" {
		callID = acc.itemToCallID[itemID]
	}
	if callID == "" {
		return item, nil, "", false
	}
	st := acc.toolStates[callID]
	if st == nil && itemID != "" {
		st = acc.toolStates[itemID]
		if st != nil {
			callID = st.id
		}
	}
	if st == nil {
		st = &responsesToolState{id: callID, name: name}
		acc.toolStates[callID] = st
	}
	if itemID != "" {
		st.itemID = itemID
		acc.itemToCallID[itemID] = st.id
		acc.toolStates[itemID] = st
	}
	if st.name == "" && name != "" {
		st.name = name
	}
	if argsStr != "" {
		st.args.Reset()
		st.args.WriteString(argsStr)
	}
	if argsStr == "" {
		argsStr = st.args.String()
	}
	acc.output = append(acc.output, map[string]any{
		"type":      "function_call",
		"call_id":   st.id,
		"id":        st.itemID,
		"name":      st.name,
		"arguments": argsStr,
	})
	return item, st, argsStr, true
}

// responsesInbandError decodes a Responses-API failure event delivered on an
// HTTP 200 stream into a typed error, or returns nil when the event carries no
// error payload. The flat "error" event holds the error fields at the top
// level; "response.failed" nests them under response.error.
func responsesInbandError(data []byte) error {
	var event struct {
		Message  string          `json:"message"`
		Code     json.RawMessage `json:"code"`
		Response struct {
			Error *openaichat.InbandError `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil //nolint:nilerr // an undecodable event keeps the raw-passthrough path
	}
	inband := event.Response.Error
	if inband == nil {
		if strings.TrimSpace(event.Message) == "" && len(event.Code) == 0 {
			return nil
		}
		inband = &openaichat.InbandError{Message: event.Message, Code: event.Code}
	}
	// Normalize the event into the {"error": {...}} envelope the typed-error
	// constructor reads the error code from, keeping the rest of the payload
	// for forensics.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		raw = map[string]any{}
	}
	var errObj map[string]any
	if b, err := json.Marshal(inband); err == nil {
		_ = json.Unmarshal(b, &errObj)
	}
	raw["error"] = errObj
	return inbandStreamError("responses.create(stream)", inband, raw)
}

// decodeResponsesStream consumes the Responses API SSE stream in its own
// goroutine: it translates each event into llm stream events and emits the final
// accumulated Response on response.completed. It emits errEmptyResponsesStream if
// the stream closes 200 OK with zero content. It owns closing the response body
// and the ChanStream.
func (a *Adapter) decodeResponsesStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, _ []byte, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	textID := "text_1"
	textStarted := false
	reasoningStarted := false
	finished := false
	var finalEvent *llm.StreamEvent
	// sentContent tracks whether any meaningful content event was emitted.
	// If the stream closes without content, we emit errEmptyResponsesStream.
	sentContent := false
	acc := newResponsesOutputAccumulator()

	parseErr := llm.ParseSSE(sctx, resp.Body, func(ev llm.SSEEvent) error {
		if len(ev.Data) == 0 {
			return nil
		}
		var payload map[string]any
		dec := json.NewDecoder(bytes.NewReader(ev.Data))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			// Emit raw passthrough and continue; returning the error would
			// abort the whole stream on a single malformed event.
			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
			return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
		}
		typ, _ := payload["type"].(string)
		if typ == "" {
			typ = ev.Event
		}

		switch typ {
		case "response.output_item.added":
			if item, ok := payload["item"].(map[string]any); ok {
				acc.HandleOutputItemAdded(item)
			}
		case "response.output_text.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				delta, _ = payload["text"].(string)
			}
			if delta == "" {
				return nil
			}
			sentContent = true
			if !textStarted {
				textStarted = true
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
			}
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
		case "response.reasoning_summary_text.delta":
			delta, _ := payload["delta"].(string)
			if delta == "" {
				return nil
			}
			sentContent = true
			s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
		case "response.reasoning_summary_part.added":
			// Detailed reasoning arrives as multiple summary parts; separate
			// them with a blank line so the rendered thought stays readable.
			if reasoningStarted {
				s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "\n\n"})
			}
			reasoningStarted = true
		case "response.function_call_arguments.delta":
			st, delta, ok := acc.HandleFunctionCallArgumentsDelta(payload)
			if !ok {
				// Can't map reliably; pass through.
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
				return nil
			}
			if !st.started {
				sentContent = true
				st.started = true
				tc := llm.ToolCallData{ID: st.id, ItemID: st.itemID, Name: st.name, Type: "function"}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
			}
			tc := llm.ToolCallData{ID: st.id, ItemID: st.itemID, Name: st.name, Arguments: []byte(delta), Type: "function"}
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &tc})
		case "response.function_call_arguments.done":
			if _, ok := acc.HandleFunctionCallArgumentsDone(payload); !ok {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
		case "response.output_item.done":
			rawItem, st, argsStr, ok := acc.HandleOutputItemDone(payload)
			switch {
			case ok:
				if !st.started {
					sentContent = true
					st.started = true
					tc := llm.ToolCallData{ID: st.id, ItemID: st.itemID, Name: st.name, Type: "function"}
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &tc})
				}
				tc := llm.ToolCallData{ID: st.id, ItemID: st.itemID, Name: st.name, Arguments: json.RawMessage(argsStr), Type: "function"}
				s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
			case rawItem == nil:
				// Best-effort: treat as end-of-text.
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
			default:
				if it, _ := rawItem["type"].(string); it == "function_call" {
					// function_call with no resolvable call_id.
					s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
				} else if textStarted {
					// Best-effort: treat as end-of-text.
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
			}
		case "response.completed":
			// Response object may be nested under "response" or be the payload itself.
			rawResp, _ := payload["response"].(map[string]any)
			if rawResp == nil {
				rawResp = payload
			}
			r := fromResponses(rawResp, req.Model)
			settleResponsesTerminalOutput(&r, rawResp, acc.Output())
			a.stampResponseIDHash(&r)
			llm.StampEndpointURL(&r, llm.FinalResponseEndpointURL(resp, a.responsesURL()), a.apiLogCredentialMaterial(nil))
			// Ensure text segment is closed.
			if textStarted {
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
				textStarted = false
			}
			sentContent = true
			rp := r
			event := llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp}
			if attempt.Active() {
				finalEvent = &event
			} else {
				s.Send(event)
			}
			// Stop parsing after finish.
			finished = true
			cancel()
		case "error", "response.failed":
			// The Responses API reports a mid-stream failure either as a flat
			// "error" event or as an error nested in the response object of
			// "response.failed". Decode it into the typed error hierarchy and
			// end the stream with it: the raw passthrough below would drop the
			// payload, leaving a content-free failure indistinguishable from an
			// unsupported-endpoint empty stream (silently retried on Chat
			// Completions) and a mid-content failure degraded to the generic
			// incomplete-stream error.
			if inband := responsesInbandError(ev.Data); inband != nil {
				return &transport.FatalStreamError{Err: inband}
			}
			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
		default:
			s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
		}
		return nil
	}, llm.StreamReadSSEOptions(req.AdapterTimeout)...)

	var terminalErr error
	var fatal *transport.FatalStreamError
	if !finished {
		switch {
		case sctx.Err() != nil:
			terminalErr = llm.WrapContextError("openai", sctx.Err())
		case errors.As(parseErr, &fatal):
			// The provider reported a structured failure in-band; the decoded,
			// typed error is the stream's terminal error in its own right.
			terminalErr = fatal.Err
		case !sentContent:
			// Stream closed 200 OK with zero content: model likely does not
			// support /v1/responses. Signal the caller to try Chat Completions.
			terminalErr = errEmptyResponsesStream
		default:
			// Content was streamed, then the stream ended without completion —
			// a genuine mid-stream read failure; surface its cause.
			terminalErr = llm.NewStreamError("openai", "openai responses stream ended without completion", parseErr)
		}
	}
	var response *llm.Response
	if finalEvent != nil {
		response = finalEvent.Response
	}
	decodeErr := terminalErr
	if finished {
		decodeErr = nil
	}
	timeoutSource := llm.APITimeoutSourceForSSE(parseErr)
	outcome := apilog.AttemptOutcomeClass("")
	if !finished && sctx.Err() == context.Canceled {
		outcome = apilog.AttemptCallerCancel
	}
	attempt.Complete(llm.APIAttemptResult{
		StatusCode: resp.StatusCode,
		Response:   response,
		Outcome:    outcome,
		Err:        terminalErr,
	}, timeoutSource, decodeErr, nil)
	if finalEvent != nil {
		s.Send(*finalEvent)
	} else if terminalErr != nil {
		s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: terminalErr})
	}
}

func toResponsesResponseFormat(rf llm.ResponseFormat) any {
	switch strings.ToLower(strings.TrimSpace(rf.Type)) {
	case "", "text":
		return nil
	case "json":
		return map[string]any{"type": "json"}
	case "json_schema":
		// Responses API requires a name for json_schema output and expects the
		// actual JSON Schema under "schema".
		return map[string]any{
			"type":   "json_schema",
			"name":   "output",
			"schema": rf.JSONSchema,
			"strict": rf.Strict,
		}
	default:
		return nil
	}
}

func toResponsesTools(tools []llm.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		strict := true
		if t.Strict != nil {
			strict = *t.Strict
		}
		if strict {
			// OpenAI strict mode requires a fully-specified JSON Schema:
			// - object schemas must set additionalProperties=false
			// - required must include every key in properties (even for "optional" fields)
			// See API validation errors like:
			// "Invalid schema for function 'read_file': ... 'required' ... Missing 'limit'."
			params = strictifyJSONSchema(params)
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
			"strict":      strict,
		})
	}
	return out
}

func strictifyJSONSchema(in map[string]any) map[string]any {
	// Best-effort deep copy + strictification for OpenAI tool schemas.
	// This intentionally handles only the constructs we emit (object/array) and is safe to
	// apply repeatedly (idempotent for our shapes).
	cp := deepCopyAny(in).(map[string]any)
	strictifyJSONSchemaInPlace(cp)
	return cp
}

func strictifyJSONSchemaInPlace(m map[string]any) {
	if m == nil {
		return
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		// OpenAI strict mode requires this be present and false.
		m["additionalProperties"] = false

		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			m["properties"] = props
		}
		// Required must include all properties keys (even for "optional" fields).
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m["required"] = keys

		// Recurse into property schemas.
		for _, k := range keys {
			if child, ok := props[k].(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			strictifyJSONSchemaInPlace(items)
		}
	}

	// If the schema uses combinators, strictify any subschemas we can find.
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	}
}

func deepCopyAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCopyAny(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyAny(x[i])
		}
		return out
	default:
		return v
	}
}

// toResponsesToolChoice converts a tool choice to the OpenAI Responses API wire
// shape. A forced function is expressed as {"type":"function","name":"X"} — the
// function name lives at the TOP LEVEL. This differs from Chat Completions, which
// nests it as {"type":"function","function":{"name":"X"}} (see
// toChatCompletionsToolChoice); sending the nested shape to /v1/responses is
// rejected with "missing required parameter: 'tool_choice.name'".
func toResponsesToolChoice(tc llm.ToolChoice) (any, error) {
	switch strings.ToLower(strings.TrimSpace(tc.Mode)) {
	case "", "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "required":
		return "required", nil
	case "named":
		if strings.TrimSpace(tc.Name) == "" {
			return nil, &llm.ConfigurationError{Message: "tool_choice mode=named requires name"}
		}
		return map[string]any{"type": "function", "name": tc.Name}, nil
	default:
		// Backward-compatible: some callers may have used an unspecified mode to force
		// a particular tool. Prefer explicit mode="named".
		if strings.TrimSpace(tc.Name) != "" {
			return map[string]any{"type": "function", "name": tc.Name}, nil
		}
		return nil, llm.NewUnsupportedToolChoiceError("openai", tc.Mode)
	}
}

// responsesLiteModel reports whether the model runs on OpenAI's "responses-lite"
// backend variant (the gpt-5.6 family). Responses-lite models mishandle the
// image "detail" field, so requests to them must omit it entirely — even when a
// caller set an explicit detail — mirroring the first-party codex client, which
// strips detail from every image on these models. They also expect the
// reasoning object (and reasoning.encrypted_content in include) on every
// request. Prompt-cache TTL is left at the public API's 30m default instead of
// translating the legacy prompt_cache_retention policy.
func responsesLiteModel(model string) bool {
	return strings.HasPrefix(model, "gpt-5.6")
}

// codexModelVariants maps a caller-facing gpt-5.6 slug to the wire slug the
// Codex backend accepts. Grounded in llm/data/litellm_model_catalog.json's
// exhaustive gpt-5.6* entries (exactly gpt-5.6, gpt-5.6-sol, gpt-5.6-terra,
// gpt-5.6-luna, confirmed by grep) — a future added variant must update both
// the catalog data and this table together. The bare "gpt-5.6" slug maps to
// "gpt-5.6-sol" (the highest-priority variant in codex's model manifest);
// every other cataloged variant maps to itself.
var codexModelVariants = map[string]string{
	"gpt-5.6":       "gpt-5.6-sol",
	"gpt-5.6-sol":   "gpt-5.6-sol",
	"gpt-5.6-terra": "gpt-5.6-terra",
	"gpt-5.6-luna":  "gpt-5.6-luna",
}

// wireModel returns the model slug to send on the wire. The ChatGPT codex
// backend has no bare "gpt-5.6" slug — it rejects it with "not supported when
// using Codex with a ChatGPT account" — because the codex CLI always sends a
// full variant slug. Map bare gpt-5.6 to the default variant via
// codexModelVariants. The platform API serves bare gpt-5.6 directly, so
// api-key requests keep the caller's slug.
func (a *Adapter) wireModel(model string) string {
	if a.usesCodexBackend() {
		if wire, ok := codexModelVariants[model]; ok {
			return wire
		}
	}
	return model
}

// defaultImageDetail returns the best image detail level for the model.
// GPT-5.4+ supports "original" (full fidelity); older models use "high".
func defaultImageDetail(model string) string {
	if strings.HasPrefix(model, "gpt-5.4") || strings.HasPrefix(model, "gpt-5.5") ||
		strings.HasPrefix(model, "gpt-6") {
		return "original"
	}
	return "high"
}

// reasoningSummaryLevel returns the reasoning.summary level to request for the
// model. The Responses API rejects an unsupported summary level with a 400, and
// support is per-model (e.g. the computer-use model only does "concise"), so we
// cannot send "detailed" unconditionally.
//
// gpt-5.x exposes "detailed" plaintext summaries, which the serf live-thinking
// block depends on (verified live on gpt-5.5). For every other reasoning model
// we send "auto": the OpenAI docs define "auto" as equivalent to "detailed" for
// most reasoning models today while letting the API pick a supported level, so
// it never 400s and still yields the richest summary the model offers.
func reasoningSummaryLevel(model string) string {
	if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") {
		return "detailed"
	}
	return "auto"
}

func toResponsesInput(msgs []llm.Message, model string) (instructions string, items []any, _ error) {
	var instrParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if t := strings.TrimSpace(m.Text()); t != "" {
				instrParts = append(instrParts, t)
			}
		}
	}
	instructions = strings.Join(instrParts, "\n\n")

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			continue
		case llm.RoleUser, llm.RoleAssistant:
			if m.Role == llm.RoleAssistant {
				// Group text parts by phase, emitting one message item per group.
				type textGroup struct {
					phase   string
					content []any
				}
				var groups []textGroup
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if strings.TrimSpace(p.Text) == "" && p.Phase == "" {
							continue
						}
						entry := map[string]any{"type": "output_text", "text": p.Text}
						if len(groups) > 0 && groups[len(groups)-1].phase == p.Phase {
							groups[len(groups)-1].content = append(groups[len(groups)-1].content, entry)
						} else {
							groups = append(groups, textGroup{phase: p.Phase, content: []any{entry}})
						}
					case llm.ContentAudio, llm.ContentDocument:
						return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
					default:
						// ignore (tool calls, images, web search handled separately)
					}
				}
				for _, g := range groups {
					item := map[string]any{
						"type":    "message",
						"role":    "assistant",
						"content": g.content,
					}
					if g.phase != "" {
						item["phase"] = g.phase
					}
					items = append(items, item)
				}
			} else {
				// User messages: no phase grouping needed.
				content := make([]any, 0, len(m.Content))
				for _, p := range m.Content {
					switch p.Kind {
					case llm.ContentText:
						if strings.TrimSpace(p.Text) == "" {
							continue
						}
						content = append(content, map[string]any{
							"type": "input_text",
							"text": p.Text,
						})
					case llm.ContentImage:
						if p.Image == nil {
							continue
						}
						url := strings.TrimSpace(p.Image.URL)
						if len(p.Image.Data) > 0 {
							mt := strings.TrimSpace(p.Image.MediaType)
							if mt == "" {
								mt = "image/png"
							}
							url = llm.DataURI(mt, p.Image.Data)
						} else if llm.IsLocalPath(url) {
							path := llm.ExpandTilde(url)
							b, err := os.ReadFile(path)
							if err != nil {
								return "", nil, err
							}
							mt := strings.TrimSpace(p.Image.MediaType)
							if mt == "" {
								mt = llm.InferMimeTypeFromPath(path)
							}
							if mt == "" {
								mt = "image/png"
							}
							url = llm.DataURI(mt, b)
						}
						if url != "" {
							img := map[string]any{
								"type":      "input_image",
								"image_url": url,
							}
							// Responses-lite models reject/mishandle detail;
							// omit it there even when explicitly set.
							if !responsesLiteModel(model) {
								if p.Image.Detail != "" {
									img["detail"] = p.Image.Detail
								} else {
									img["detail"] = defaultImageDetail(model)
								}
							}
							content = append(content, img)
						}
					case llm.ContentDocument:
						if p.Document == nil {
							continue
						}
						var fileData string
						if len(p.Document.Data) > 0 {
							mt := strings.TrimSpace(p.Document.MediaType)
							if mt == "" {
								mt = "application/pdf"
							}
							fileData = llm.DataURI(mt, p.Document.Data)
						} else if p.Document.URL != "" {
							fileData = p.Document.URL
						}
						if fileData != "" {
							entry := map[string]any{
								"type":      "input_file",
								"file_data": fileData,
							}
							if p.Document.FileName != "" {
								entry["filename"] = p.Document.FileName
							}
							content = append(content, entry)
						}
					case llm.ContentAudio:
						return "", nil, &llm.ConfigurationError{Message: fmt.Sprintf("unsupported content kind for openai: %s", p.Kind)}
					default:
						// ignore (tool calls are top-level items)
					}
				}
				if len(content) > 0 {
					items = append(items, map[string]any{
						"type":    "message",
						"role":    "user",
						"content": content,
					})
				}
			}
			for _, p := range m.Content {
				if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.EncryptedContent != "" &&
					!llm.IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
					// The guard skips OpenRouter-style encrypted reasoning_details
					// riding a cross-provider transcript — those are not OpenAI
					// Responses encrypted_content blobs and the API rejects them.
					item := map[string]any{
						"type":              "reasoning",
						"encrypted_content": p.Thinking.EncryptedContent,
						"summary":           reasoningSummaryInput(p.Thinking.Summary),
					}
					if strings.TrimSpace(p.Thinking.ID) != "" {
						item["id"] = strings.TrimSpace(p.Thinking.ID)
					}
					items = append(items, item)
				}
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   p.ToolCall.ID,
						"name":      p.ToolCall.Name,
						"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
					})
				}
			}
			for _, p := range m.Content {
				if p.Kind == llm.ContentWebSearch && p.WebSearch != nil && len(p.WebSearch.Raw) > 0 {
					var rawItem map[string]any
					if err := json.Unmarshal(p.WebSearch.Raw, &rawItem); err == nil {
						items = append(items, rawItem)
					}
				}
			}
		case llm.RoleTool:
			for _, p := range m.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				outStr := ""
				if p.ToolResult.IsError {
					// OpenAI Responses API rejects unknown params like "is_error" on
					// function_call_output items. Preserve error semantics by wrapping
					// the original content in a JSON string.
					wrapped := map[string]any{
						"is_error": true,
						"content":  p.ToolResult.Content,
					}
					if b, err := json.Marshal(wrapped); err == nil {
						outStr = string(b)
					} else {
						outStr = fmt.Sprintf(`{"is_error":true,"content":%q}`, fmt.Sprint(p.ToolResult.Content))
					}
				} else {
					switch v := p.ToolResult.Content.(type) {
					case string:
						outStr = v
					default:
						b, _ := json.Marshal(v)
						outStr = string(b)
					}
				}
				item := map[string]any{
					"type":    "function_call_output",
					"call_id": p.ToolResult.ToolCallID,
					"output":  outStr,
				}

				if len(p.ToolResult.ImageData) > 0 {
					mt := p.ToolResult.ImageMediaType
					if mt == "" {
						mt = "image/png"
					}
					img := map[string]any{
						"type":      "input_image",
						"image_url": llm.DataURI(mt, p.ToolResult.ImageData),
					}
					// Responses-lite models reject/mishandle detail; omit it there.
					if !responsesLiteModel(model) {
						img["detail"] = defaultImageDetail(model)
					}
					item["output"] = []any{
						map[string]any{"type": "input_text", "text": outStr},
						img,
					}
				}
				items = append(items, item)
			}
		default:
			// ignore unknown roles
		}
	}
	return instructions, items, nil
}

func reasoningSummaryInput(summary []string) []any {
	out := make([]any, 0, len(summary))
	for _, text := range summary {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, map[string]any{
			"type": "summary_text",
			"text": text,
		})
	}
	return out
}

func parseReasoningSummary(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// responseContentFromOutputItems converts a Responses API output-item array
// into message content parts. It walks the same wire shape whether the items
// come from a terminal response.completed payload's "output" field or from
// decodeResponsesStream's accumulated response.output_item.done events, so
// both callers share this one walk.
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
			if encryptedContent != "" {
				content = append(content, llm.ContentPart{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						ID:               id,
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
// Shared by the live streaming decoder (decodeResponsesStream) and offline
// recomputation (ExtractRecordedResponse) so both apply the identical
// terminal-wins rule.
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
