package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

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
// (decodeStream) and offline recomputation, so the two can't silently drift
// apart.
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
// level; "response.failed" nests them under response.error. The error's
// provider starts as "openai" (matching the non-streaming classifier's
// convention for this wire shape) and decodeStream rewrites it to the
// instance's own name before it reaches the caller.
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
	msg := strings.TrimSpace(inband.Message)
	if msg == "" {
		msg = "provider reported an in-band stream error"
	}
	return llm.ErrorFromHTTPStatus("openai", inband.StatusCode(), "responses.create(stream): "+msg, raw, nil)
}

// responsesAPIEventTypes are the event types this decoder recognizes as the
// Responses API's own: the ones the decode switch below handles, plus the
// lifecycle events OpenAI documents ahead of the first delta. An endpoint that
// does not implement the Responses API cannot emit any of them, which is what
// makes them proof that the endpoint served the request.
//
// Two deliberate exclusions keep that proof honest. An unrecognized
// "response.*" type is not enough: it is forwarded raw precisely because this
// decoder does not know what it is, so it cannot vouch for the endpoint. A bare
// "error" envelope is not enough either — it is generic SSE, not this API. Both
// leave the empty-stream sentinel armed.
var responsesAPIEventTypes = map[string]struct{}{
	"response.created":                       {},
	"response.in_progress":                   {},
	"response.content_part.added":            {},
	"response.output_item.added":             {},
	"response.output_item.done":              {},
	"response.output_text.delta":             {},
	"response.reasoning_summary_part.added":  {},
	"response.reasoning_summary_text.delta":  {},
	"response.function_call_arguments.delta": {},
	"response.function_call_arguments.done":  {},
	"response.completed":                     {},
	"response.failed":                        {},
}

// decodeStream consumes the Responses API SSE stream in its own goroutine:
// it translates each event into llm stream events and emits the final
// accumulated Response on response.completed. It emits a terminal stream
// error if the stream closes cleanly, 200 OK, without a single recognized
// Responses API event. It owns closing the response body and the ChanStream.
func (p *Protocol) decodeStream(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, req llm.Request, res registry.Resolved, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	textID := "text_1"
	textStarted := false
	reasoningStarted := false
	finished := false
	var finalEvent *llm.StreamEvent
	// sawResponsesEvent records an event this decoder recognizes as belonging
	// to the Responses API. It is the whole evidence the empty-stream sentinel
	// rests on: an endpoint that does not implement the API cannot produce one,
	// so seeing any of them proves the endpoint served this model — long before
	// content arrives, which at high reasoning effort can take a while.
	sawResponsesEvent := false
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
		if _, ok := responsesAPIEventTypes[typ]; ok {
			sawResponsesEvent = true
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
			built := fromResponses(rawResp, req.Model)
			settleResponsesTerminalOutput(&built, rawResp, acc.Output())
			built.Provider = res.Instance
			p.stampResponseIDHash(sctx, &built)
			llm.StampEndpointURL(&built, r.EndpointURL, r.Material)
			// Ensure text segment is closed.
			if textStarted {
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
				textStarted = false
			}
			rp := built
			event := llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &built.Finish, Usage: &built.Usage, Response: &rp}
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
			// unsupported-endpoint empty stream and a mid-content failure
			// degraded to the generic incomplete-stream error.
			if inband := responsesInbandError(ev.Data); inband != nil {
				return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(inband, res.Instance)}
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
		// Every read failure is handled before the empty-stream sentinel, so
		// the sentinel can only fire for a stream that closed cleanly. A read
		// that broke, timed out, or was cancelled is evidence about the
		// transport; only a clean close with nothing recognized on it is
		// evidence about what the endpoint implements.
		switch {
		case sctx.Err() != nil:
			terminalErr = llm.WrapContextError(res.Instance, sctx.Err())
		case errors.As(parseErr, &fatal):
			// The provider reported a structured failure in-band; the decoded,
			// typed error is the stream's terminal error in its own right.
			terminalErr = fatal.Err
		case errors.Is(parseErr, llm.ErrSSEReadTimeout):
			// The stream went idle past the read timeout. Named separately from
			// the read failures below because a stall is the one an operator
			// most often has to tell apart from a broken connection.
			terminalErr = llm.NewStreamError(res.Instance, "responses stream stalled without completion", parseErr)
		case parseErr != nil:
			// The read failed. Surface its cause.
			terminalErr = llm.NewStreamError(res.Instance, "responses stream ended without completion", parseErr)
		case !sawResponsesEvent:
			// The stream closed cleanly, 200 OK, without a single event this
			// decoder recognizes as the Responses API: the model likely does
			// not support /v1/responses. There is no Chat Completions fallback
			// in this transport, so this is permanent by construction — the
			// retry chain short-circuits on it and the caller routes to its
			// next model.
			terminalErr = llm.NewUnsupportedEndpointError(res.Instance, "responses stream closed with no events", nil)
		default:
			// The endpoint served real Responses events and then closed cleanly
			// without response.completed — a truncated response, with no read
			// error to report as its cause.
			terminalErr = llm.NewStreamError(res.Instance, "responses stream closed before response.completed", nil)
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
	if timeoutSource == llm.APITimeoutNone {
		timeoutSource = attempt.TimeoutSource()
	}
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
