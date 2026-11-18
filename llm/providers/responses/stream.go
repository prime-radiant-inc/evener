package responses

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

type responsesToolState struct {
	id, itemID, name   string
	started, hasOutput bool
	args               responsesOutputText
}

type responsesOutputItem struct {
	kind, id, phase string
	index           int64
	done            map[string]any
	doneOrder       int
	raw             json.RawMessage
	text            map[int64]*responsesOutputText
	tool            *responsesToolState
	display         responsesFragments
	displayOnly     bool
}

// Fragments from different provisional items can interleave before the provider
// links their identifiers. Adjacent writes to the same part share one buffer.
type responsesFragment struct {
	order int
	data  []byte
}
type responsesFragments []responsesFragment

func (acc *responsesOutputAccumulator) write(f *responsesFragments, text string) {
	if n := len(*f); n > 0 {
		last := &(*f)[n-1]
		if last.order == acc.order && len(last.data) != 0 && text != "" {
			last.data = append(last.data, text...)
			return
		}
	}
	acc.order++
	*f = append(*f, responsesFragment{acc.order, []byte(text)})
}

func (f responsesFragments) value() string {
	if len(f) == 1 {
		return string(f[0].data)
	}
	var text strings.Builder
	var displayPart bool
	for _, fragment := range f {
		if len(fragment.data) != 0 {
			text.Write(fragment.data)
		} else {
			// An empty fragment marks the start of a display reasoning part.
			if displayPart {
				text.WriteString(reasoningPartSeparator)
			}
			displayPart = true
		}
	}
	return text.String()
}

func (f *responsesFragments) merge(other responsesFragments) {
	if len(other) != 0 {
		*f = append(*f, other...)
		slices.SortFunc(*f, func(a, b responsesFragment) int { return cmp.Compare(a.order, b.order) })
	}
}

type responsesOutputText struct {
	deltas     responsesFragments
	final      string
	finalOrder int
	itemFinal  bool
}

func (p *responsesOutputText) value() string {
	if p.final != "" {
		return p.final
	}
	return p.deltas.value()
}

func (item *responsesOutputItem) textPart(index int64, single bool) *responsesOutputText {
	if single && index < 0 && len(item.text) > 1 {
		return nil
	}
	if single && len(item.text) == 1 {
		for known, part := range item.text {
			if index < 0 {
				return part
			}
			if known < 0 {
				delete(item.text, known)
				item.text[index] = part
			}
		}
	}
	if item.text == nil {
		item.text = make(map[int64]*responsesOutputText)
	}
	if item.text[index] == nil {
		item.text[index] = new(responsesOutputText)
	}
	return item.text[index]
}

func (p *responsesOutputText) settle(value string, itemFinal bool, order int) {
	if value != "" && (itemFinal || !p.itemFinal) && (itemFinal != p.itemFinal || order >= p.finalOrder) {
		p.final, p.itemFinal, p.finalOrder = value, itemFinal, order
		p.deltas = nil
	}
}

func (p *responsesOutputText) merge(other *responsesOutputText) {
	if other.final != "" {
		p.settle(other.final, other.itemFinal, other.finalOrder)
	} else if p.final == "" {
		p.deltas.merge(other.deltas)
	}
}

// responsesOutputAccumulator retains completed items and pending answer and
// tool output when a terminal response omits them. The live decoder and
// recorded SSE reader use the same item identities and settlement rules.
type responsesOutputAccumulator struct {
	byCallID         map[string]*responsesOutputItem
	output           []*responsesOutputItem
	byID             map[string]*responsesOutputItem
	byIndex          map[int64]*responsesOutputItem
	completed        int
	order            int
	anonymousDisplay *responsesOutputItem
	reasoningStarted bool
}

func (acc *responsesOutputAccumulator) reasoningDisplay(typ string, payload map[string]any) string {
	added := typ == "response.reasoning_part.added" || typ == "response.reasoning_summary_part.added"
	delta, _ := payload["delta"].(string)
	if !added && delta == "" {
		return ""
	}
	id, _ := payload["item_id"].(string)
	source := acc.outputItem(payload, "reasoning", id, nil, false)
	acc.write(&source.display, delta)
	if added {
		if acc.reasoningStarted {
			return reasoningPartSeparator
		}
		acc.reasoningStarted = true
		return ""
	}
	return delta
}

func newResponsesOutputAccumulator() *responsesOutputAccumulator {
	return &responsesOutputAccumulator{
		byCallID: map[string]*responsesOutputItem{},
		byID:     map[string]*responsesOutputItem{},
		byIndex:  map[int64]*responsesOutputItem{},
	}
}

func responsesEventIndex(payload map[string]any, field string) int64 {
	if n, ok := payload[field].(json.Number); ok {
		if i, err := n.Int64(); err == nil && i >= 0 {
			return i
		}
	}
	return -1
}

func (item *responsesOutputItem) accepts(kind, id string, index int64) bool {
	return item != nil && item.kind == kind && (item.id == "" || id == "" || id == item.id) &&
		(item.index < 0 || index < 0 || index == item.index)
}

func (acc *responsesOutputAccumulator) namedItem(kind, id string) *responsesOutputItem {
	item := acc.byID[id]
	if !item.accepts(kind, id, -1) {
		return nil
	}
	if item.tool != nil && item.id == "" {
		// An earlier unqualified delta ID is now confirmed as the item ID.
		item.id, item.tool.itemID = id, id
	}
	return item
}

// One native event can link two previously disjoint identities. A joined
// item owns their fields and keeps the first observed place in the stream.
func (acc *responsesOutputAccumulator) join(item, other *responsesOutputItem) *responsesOutputItem {
	if item == nil {
		return other
	}
	if other == nil || other == item || !other.accepts(item.kind, item.id, item.index) {
		return item
	}
	if a, b := item.tool, other.tool; a != nil && b != nil {
		if a.id != a.itemID && b.id != b.itemID && a.id != b.id {
			return item
		}
		if a.id == a.itemID {
			a.id = b.id
		}
		a.itemID = cmp.Or(a.itemID, b.itemID)
		a.name = cmp.Or(a.name, b.name)
		a.started = a.started || b.started
		a.hasOutput = a.hasOutput || b.hasOutput
		a.args.merge(&b.args)
	} else if item.tool == nil {
		item.tool = other.tool
	}
	item.id = cmp.Or(item.id, other.id)
	item.phase = cmp.Or(item.phase, other.phase)
	item.index = max(item.index, other.index)
	item.displayOnly = item.displayOnly && other.displayOnly
	item.display.merge(other.display)
	if other.doneOrder > item.doneOrder {
		item.done, item.raw, item.doneOrder = other.done, other.raw, other.doneOrder
	}
	for index, part := range other.text {
		item.textPart(index, false).merge(part)
	}
	if unknown := item.text[-1]; unknown != nil && len(item.text) == 2 {
		delete(item.text, -1)
		for _, only := range item.text {
			only.merge(unknown)
		}
	}
	if item.index >= 0 {
		acc.byIndex[item.index] = item
	}
	for _, aliases := range []map[string]*responsesOutputItem{acc.byID, acc.byCallID} {
		for id, observed := range aliases {
			if observed == other {
				aliases[id] = item
			}
		}
	}
	first, last := slices.Index(acc.output, item), slices.Index(acc.output, other)
	if first > last {
		first, last = last, first
	}
	acc.output[first] = item
	acc.output = slices.Delete(acc.output, last, last+1)
	return item
}

// An item can be identified before its contents arrive. Retain the position
// supplied by any of its events and keep first appearance for unindexed items.
func (acc *responsesOutputAccumulator) outputItem(payload map[string]any, kind, id string, fallback *responsesOutputItem, native bool) *responsesOutputItem {
	index := responsesEventIndex(payload, "output_index")
	named, indexed := acc.byID[id], acc.byIndex[index]
	if !native {
		if (named != nil && named.kind != kind) || (indexed != nil && indexed.kind != kind) {
			id, index = "", -1 // A display delta cannot claim a different native output.
			indexed = nil
		}
		if id == "" && index < 0 {
			fallback = acc.anonymousDisplay
		}
	}
	named = acc.namedItem(kind, id)
	if !indexed.accepts(kind, id, index) {
		indexed = nil
	}
	item := acc.join(named, indexed)
	if fallback.accepts(kind, id, index) {
		item = acc.join(item, fallback)
	}
	if item == nil {
		item = &responsesOutputItem{kind: kind, index: -1, displayOnly: !native}
		acc.output = append(acc.output, item)
	} else if native && item.displayOnly {
		// Unindexed native items are ordered by their first native event.
		if item.index < 0 && index < 0 {
			i := slices.Index(acc.output, item)
			acc.output = append(slices.Delete(acc.output, i, i+1), item)
		}
		item.displayOnly = false
	}
	if id != "" {
		item.id = id
		acc.byID[id] = item
	}
	if index >= 0 && (acc.byIndex[index] == nil || acc.byIndex[index] == item) && (item.index < 0 || item.index == index) {
		item.index = index
		acc.byIndex[index] = item
	}
	if !native && id == "" && index < 0 {
		acc.anonymousDisplay = item
	}
	return item
}

// Missing identity can be filled only when one compatible message exists.
func (acc *responsesOutputAccumulator) uniqueMessage(id string, index int64, pendingOnly bool) (*responsesOutputItem, bool) {
	var candidate *responsesOutputItem
	for _, item := range acc.output {
		if !item.accepts("message", id, index) || (pendingOnly && item.done != nil) {
			continue
		}
		if candidate != nil {
			return nil, false
		}
		candidate = item
	}
	return candidate, true
}

func (acc *responsesOutputAccumulator) textOutput(payload map[string]any) *responsesOutputItem {
	id, _ := payload["item_id"].(string)
	var fallback *responsesOutputItem
	if id == "" && responsesEventIndex(payload, "output_index") < 0 {
		var unambiguous bool
		fallback, unambiguous = acc.uniqueMessage("", -1, true)
		if !unambiguous {
			return nil // Visible to the user; its owning message is ambiguous.
		}
	}
	return acc.outputItem(payload, "message", id, fallback, true)
}

func (acc *responsesOutputAccumulator) handleOutputTextDelta(payload map[string]any) string {
	delta, _ := payload["delta"].(string)
	if delta == "" {
		delta, _ = payload["text"].(string)
	}
	if delta != "" {
		if item := acc.textOutput(payload); item != nil {
			p := item.textPart(responsesEventIndex(payload, "content_index"), true)
			if p != nil && p.final == "" {
				acc.write(&p.deltas, delta)
			}
		}
	}
	return delta
}

func (acc *responsesOutputAccumulator) handleTextPartDone(payload map[string]any, typ string) bool {
	part := payload
	if typ == "response.content_part.done" {
		part, _ = payload["part"].(map[string]any)
		if part["type"] != "output_text" {
			return false
		}
	}
	text, ok := part["text"].(string)
	if ok {
		if item := acc.textOutput(payload); item != nil {
			acc.order++
			if p := item.textPart(responsesEventIndex(payload, "content_index"), true); p != nil {
				p.settle(text, false, acc.order)
			}
		}
	}
	return typ == "response.output_text.done" || text != ""
}

// Completion can supply a different subset of parts from the deltas.
func (item *responsesOutputItem) settleMessage(message map[string]any, order int) {
	if phase, _ := message["phase"].(string); phase != "" {
		item.phase = phase
	}
	parts, _ := message["content"].([]any)
	for index, entry := range parts {
		part, _ := entry.(map[string]any)
		if text, ok := part["text"].(string); ok && part["type"] == "output_text" {
			item.textPart(int64(index), len(parts) == 1).settle(text, true, order)
		}
	}
}

func (acc *responsesOutputAccumulator) registeredCallID(itemID string) string {
	if item := acc.byID[itemID]; item != nil && item.tool != nil {
		return item.tool.id
	}
	return ""
}

// Both native IDs name the same call. A call known only by item ID can gain
// its call ID and function name from a later event.
func (acc *responsesOutputAccumulator) toolOutput(payload map[string]any, callID, itemID, name string) *responsesOutputItem {
	item := acc.outputItem(payload, "function_call", itemID, acc.byCallID[callID], true)
	if item.tool == nil {
		item.tool = &responsesToolState{id: callID}
	}
	st := item.tool
	if st.id == itemID {
		st.id = callID
	}
	st.name = cmp.Or(st.name, name)
	st.itemID = item.id
	acc.byCallID[st.id] = item
	return item
}

// HandleOutputItemAdded retains item positions and message phases before
// deltas arrive, and registers both identities of a function call.
func (acc *responsesOutputAccumulator) HandleOutputItemAdded(payload, item map[string]any) {
	itemID, _ := item["id"].(string)
	if kind, _ := item["type"].(string); kind != "function_call" {
		var fallback *responsesOutputItem
		if kind == "message" {
			pending, _ := acc.uniqueMessage(itemID, responsesEventIndex(payload, "output_index"), true)
			if pending != nil && pending.id == "" && pending.index < 0 && len(pending.text) != 0 {
				fallback = pending
			}
		}
		output := acc.outputItem(payload, kind, itemID, fallback, true)
		phase, _ := item["phase"].(string)
		output.phase = cmp.Or(phase, output.phase)
		return
	}
	callID, _ := item["call_id"].(string)
	name, _ := item["name"].(string)
	stateID := cmp.Or(callID, itemID)
	if stateID == "" {
		return
	}
	acc.toolOutput(payload, stateID, itemID, name)
}

// HandleFunctionCallArgumentsDelta merges one
// response.function_call_arguments.delta event's argument fragment into
// tool state, resolving call_id via item_id when call_id is absent. eventData
// preserves bytes the decoded payload string coerced; capture or unescape
// failure falls back to that string. The returned delta is the effective
// fragment written to state, for byte-identical live emission. ok is false
// when the event can't be mapped to a tool call at all -- the caller should
// pass the raw payload through unmodified in that case.
func (acc *responsesOutputAccumulator) HandleFunctionCallArgumentsDelta(payload map[string]any, eventData []byte) (state *responsesToolState, delta string, ok bool) {
	delta, _ = payload["delta"].(string)
	if delta == "" {
		delta, _ = payload["arguments"].(string)
	}
	callID, _ := payload["call_id"].(string)
	itemID, _ := payload["item_id"].(string)
	unqualified := itemID == "" && callID == ""
	if callID == "" && itemID != "" {
		callID = acc.registeredCallID(itemID)
	}
	if callID == "" {
		callID = itemID
	}
	if callID == "" {
		callID, _ = payload["id"].(string)
		if known := acc.byID[callID]; known != nil && known.tool != nil && known.id == callID {
			itemID, callID = known.id, known.tool.id
		}
	}
	name, _ := payload["name"].(string)
	if callID == "" || (delta == "" && name == "") {
		return nil, "", false
	}
	output := acc.toolOutput(payload, callID, itemID, name)
	if unqualified && output.id == "" && acc.byID[callID] == nil {
		// A bare id can later be identified as either the item_id or call_id.
		acc.byID[callID] = output
	}
	st := output.tool
	st.hasOutput = true
	if delta != "" {
		fragment := []byte(delta)
		if rawDelta, rawOK := captureResponsesDeltaRaw(eventData); rawOK && rawDelta != nil {
			if content, err := protocolhttp.RawStringContent(rawDelta); err == nil && len(content) > 0 {
				fragment = content
			}
		}
		delta = string(fragment)
		if st.args.final == "" {
			acc.write(&st.args.deltas, delta)
		}
	}
	return st, delta, true
}

// HandleFunctionCallArgumentsDone applies a
// response.function_call_arguments.done event's authoritative full-arguments
// bytes, overriding whatever deltas were accumulated so far, resolving
// call_id via item_id when needed. eventData preserves bytes the decoded
// payload string coerced; capture or unescape failure falls back to that
// string. ok is false when the event carried no resolvable call_id -- the
// caller should pass the raw payload through.
func (acc *responsesOutputAccumulator) HandleFunctionCallArgumentsDone(payload map[string]any, eventData []byte) (state *responsesToolState, ok bool) {
	argsStr, _ := payload["arguments"].(string)
	callID, _ := payload["call_id"].(string)
	itemID, _ := payload["item_id"].(string)
	if callID == "" && itemID != "" {
		callID = acc.registeredCallID(itemID)
	}
	if callID == "" {
		callID = itemID
	}
	if callID == "" {
		return nil, false
	}
	name, _ := payload["name"].(string)
	st := acc.toolOutput(payload, callID, itemID, name).tool
	if argsStr != "" {
		st.hasOutput = true
		if rawArgs, rawOK := captureResponsesArgumentsDoneRaw(eventData); rawOK && rawArgs != nil {
			if content, err := protocolhttp.RawStringContent(rawArgs); err == nil {
				argsStr = string(content)
			}
		}
		acc.order++
		st.args.settle(argsStr, false, acc.order)
	}
	return st, true
}

// HandleOutputItemDone processes a response.output_item.done event's item,
// recording its settled output. For function_call items it also resolves/updates
// tool state, using the item's own "arguments" when present or falling back
// to whatever deltas were accumulated.
//
// It returns the raw item (nil if the payload carried none -- e.g. the
// event was malformed), and, only for a function_call item whose call_id
// resolved, the tool state and its full arguments string with ok=true.
// ok=false covers both "not a function_call" (the item is recorded) and
// "function_call with no resolvable call_id" (the item is skipped).
// Callers distinguish the two by
// checking rawItem's own "type" field, matching the two different
// live-stream fallbacks (best-effort end-of-text vs. raw passthrough).
func (acc *responsesOutputAccumulator) HandleOutputItemDone(payload map[string]any, eventData []byte) (rawItem map[string]any, state *responsesToolState, argsStr string, ok bool) {
	itemAny := payload["item"]
	if itemAny == nil {
		itemAny = payload["output_item"]
	}
	item, mapOK := itemAny.(map[string]any)
	if !mapOK {
		return nil, nil, "", false
	}
	rawItemToken := captureResponsesOutputItemRaw(eventData)
	acc.order++
	it, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	if it != "function_call" {
		var fallback *responsesOutputItem
		if it == "message" && acc.byID[itemID] == nil {
			fallback, _ = acc.uniqueMessage(itemID, responsesEventIndex(payload, "output_index"), true)
		}
		output := acc.outputItem(payload, it, itemID, fallback, true)
		if it == "message" {
			output.settleMessage(item, acc.order)
		}
		output.done, output.raw = item, rawItemToken
		output.doneOrder = acc.order
		acc.completed++
		return item, nil, "", false
	}
	callID, _ := item["call_id"].(string)
	name, _ := item["name"].(string)
	argsStr, _ = item["arguments"].(string)
	if rawArgs, rawOK := captureResponsesItemArgsRaw(rawItemToken); rawOK && rawArgs != nil {
		if content, err := protocolhttp.RawStringContent(rawArgs); err == nil {
			argsStr = string(content)
		}
	}
	if callID == "" && itemID != "" {
		callID = acc.registeredCallID(itemID)
	}
	if callID == "" {
		return item, nil, "", false
	}
	st := acc.toolOutput(payload, callID, itemID, name).tool
	st.hasOutput = true
	st.args.settle(argsStr, true, acc.order)
	argsStr = st.args.value()
	acc.completed++
	return item, st, argsStr, true
}

// responsesInbandError decodes a Responses-API failure event delivered on an
// HTTP 200 stream into a typed error, or returns nil when the event carries no
// error payload. The flat "error" event holds the error fields at the top
// level; "response.failed" nests them under response.error; and gateways such
// as lunaroute wrap an upstream rejection as {"error": {...}}. The error's
// provider starts as "openai" (matching the non-streaming classifier's
// convention for this wire shape) and decodeStream rewrites it to the
// instance's own name before it reaches the caller.
func responsesInbandError(data []byte) error {
	var event struct {
		Message  string          `json:"message"`
		Code     json.RawMessage `json:"code"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error *openaichat.InbandError `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil //nolint:nilerr // an undecodable event keeps the raw-passthrough path
	}
	inband := event.Response.Error
	if inband == nil && len(event.Error) > 0 {
		// Only an object that actually names a failure counts; a provider that
		// sends "error": "some string" has told us nothing the typed error can
		// carry, and must not turn a healthy frame into a failure.
		var nested openaichat.InbandError
		if err := json.Unmarshal(event.Error, &nested); err == nil &&
			(nested.Message != "" || nested.Type != "" || len(nested.Code) > 0) {
			inband = &nested
		}
	}
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

// reasoningPartSeparator joins consecutive reasoning parts, both as they
// stream and in the settled thinking text, so the transcript reads like the
// live view.
const reasoningPartSeparator = "\n\n"

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
	"response.content_part.done":             {},
	"response.output_item.added":             {},
	"response.output_item.done":              {},
	"response.output_text.delta":             {},
	"response.output_text.done":              {},
	"response.reasoning_summary_part.added":  {},
	"response.reasoning_summary_text.delta":  {},
	"response.reasoning_summary_text.done":   {},
	"response.reasoning_part.added":          {},
	"response.reasoning_text.delta":          {},
	"response.reasoning_text.done":           {},
	"response.refusal.delta":                 {},
	"response.refusal.done":                  {},
	"response.output_audio.delta":            {},
	"response.output_audio.done":             {},
	"response.output_audio_transcript.delta": {},
	"response.output_audio_transcript.done":  {},
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
	finished := false
	// sawResponsesEvent records an event this decoder recognizes as belonging
	// to the Responses API. It is the whole evidence the empty-stream sentinel
	// rests on: an endpoint that does not implement the API cannot produce one,
	// so seeing any of them proves the endpoint served this model — long before
	// content arrives, which at high reasoning effort can take a while.
	sawResponsesEvent := false
	acc := newResponsesOutputAccumulator()

	var runner *transport.StreamRunner
	runner = &transport.StreamRunner{
		Provider:   res.Instance,
		Resp:       resp,
		Stream:     s,
		Attempt:    attempt,
		StatusCode: resp.StatusCode,
		Cancel:     cancel,
		// HTTP response-byte idle is owned by ClientWithAdapterTimeout, below gzip decoding.
		Finished: &finished,
		// This transport has four ways to end without completing, and the
		// runner's single IncompleteMsg cannot tell them apart, so it hands
		// the classification back here instead. Every read failure is handled
		// before the empty-stream sentinel, so the sentinel can only fire for
		// a stream that closed cleanly: a read that broke, timed out, or was
		// cancelled is evidence about the transport; only a clean close with
		// nothing recognized on it is evidence about what the endpoint
		// implements.
		TerminalError: func(parseErr error) error {
			switch {
			case errors.Is(parseErr, llm.ErrSSEReadTimeout), errors.Is(parseErr, llm.ErrResponseIdleTimeout):
				// The stream went idle past the read timeout. Named separately
				// from the read failures below because a stall is the one an
				// operator most often has to tell apart from a broken
				// connection.
				return llm.NewStreamError(res.Instance, "responses stream stalled without completion", parseErr)
			case parseErr != nil:
				// The read failed. Surface its cause.
				return llm.NewStreamError(res.Instance, "responses stream ended without completion", parseErr)
			case !sawResponsesEvent:
				// The stream closed cleanly, 200 OK, without a single event
				// this decoder recognizes as the Responses API: the model
				// likely does not support /v1/responses. There is no Chat
				// Completions fallback in this transport, so this is permanent
				// by construction — the retry chain short-circuits on it and
				// the caller routes to its next model.
				//
				// An error envelope never reaches here: OnEvent turns a frame
				// that names a failure into a FatalStreamError, which the
				// runner reports before consulting this hook. A gateway's
				// wrapped upstream rejection is a transient provider failure,
				// and calling it a protocol mismatch would skip the retry
				// chain the failure earns.
				return llm.NewUnsupportedEndpointError(res.Instance, "responses stream closed with no events", nil)
			default:
				// The endpoint served real Responses events and then closed
				// cleanly without response.completed — a truncated response,
				// with no read error to report as its cause.
				return llm.NewStreamError(res.Instance, "responses stream closed before response.completed", nil)
			}
		},
		OnEvent: func(ev llm.SSEEvent) error {
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
					acc.HandleOutputItemAdded(payload, item)
				}
			case "response.output_text.delta":
				delta := acc.handleOutputTextDelta(payload)
				if delta == "" {
					return nil
				}
				if !textStarted {
					textStarted = true
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
			case "response.output_text.done", "response.content_part.done":
				bearing := acc.handleTextPartDone(payload, typ)
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: bearing})
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
				"response.reasoning_summary_part.added", "response.reasoning_part.added":
				if delta := acc.reasoningDisplay(typ, payload); delta != "" {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
				}
			case "response.function_call_arguments.delta":
				st, delta, ok := acc.HandleFunctionCallArgumentsDelta(payload, ev.Data)
				if !ok {
					// Can't map reliably; pass through.
					s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: true})
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
				if _, ok := acc.HandleFunctionCallArgumentsDone(payload, ev.Data); !ok {
					s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: true})
				}
			case "response.output_item.done":
				rawItem, st, argsStr, ok := acc.HandleOutputItemDone(payload, ev.Data)
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
						s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: true})
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
				// Extract the raw response object bytes from ev.Data for
				// byte-faithful argument capture (the lossy map has already
				// coerced invalid UTF-8). When the response is nested under
				// "response", extract that sub-object's raw bytes.
				respBody := extractResponseObjectRaw(ev.Data)
				built := settleResponsesTerminalOutput(rawResp, req.Model, respBody, acc)
				built.Provider = res.Instance
				p.stampResponseIDHash(sctx, &built)
				llm.StampEndpointURL(&built, r.EndpointURL, r.Material)
				// Ensure text segment is closed.
				if textStarted {
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
					textStarted = false
				}
				runner.Finish(built)
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
				// A failure frame can miss the switch above two ways: a gateway
				// can wrap an upstream rejection as {"error": {...}} with no
				// event type at all (lunaroute does), and a flat envelope's own
				// "type" ("server_error") displaces the SSE event name, so an
				// "error" frame lands here as an unrecognized type. Either one
				// is the provider reporting a failure, not silence about what
				// the endpoint serves — and without this it falls through to the
				// empty-stream sentinel, which calls a transient upstream
				// failure a permanent protocol mismatch and skips the retry
				// chain.
				_, carriesError := payload["error"]
				failureFrame := ev.Event == "error" || typ == "error" || carriesError
				if failureFrame {
					if inband := responsesInbandError(ev.Data); inband != nil {
						return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(inband, res.Instance)}
					}
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: responsesContentBearingPassthrough(typ)})
			}
			return nil
		},
	}
	runner.Run(sctx)
}

// responsesContentBearingPassthrough reports whether a Responses event the
// decoder did not normalize into a typed stream event carries model output the
// caller renders. The adapter owns this classification because only it knows
// which event types are content rather than lifecycle: response.refusal.delta
// carries refusal text, audio/transcript deltas carry output bytes, and the
// .done siblings carry the same content in one frame. Lifecycle frames
// (response.created, response.in_progress, …) and unknown types stay
// unclassified, so a control-only stream that truncates still retries.
//
// This is a built-in-adapter contract, not a guarantee for arbitrary custom
// adapters: one that emits its own Raw payload must set
// StreamEvent.ContentBearing itself (#4941).
func responsesContentBearingPassthrough(typ string) bool {
	switch typ {
	case "response.refusal.delta", "response.refusal.done",
		"response.output_text.done",
		"response.output_audio.delta", "response.output_audio.done",
		"response.output_audio_transcript.delta", "response.output_audio_transcript.done",
		"response.reasoning_summary_text.done", "response.reasoning_text.done":
		return true
	default:
		return false
	}
}

// captureResponsesDeltaRaw decodes an SSE event's raw data with a focused
// struct that captures the selected fragment as json.RawMessage (the string
// token) rather than string, preserving bytes that json.Unmarshal into string
// would coerce. It mirrors HandleFunctionCallArgumentsDelta's field order:
// prefer a non-empty string delta, else use arguments. Returns (nil, false) if
// the focused decode fails.
func captureResponsesDeltaRaw(eventData []byte) (json.RawMessage, bool) {
	var focused struct {
		Delta     json.RawMessage `json:"delta"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(eventData, &focused); err != nil {
		return nil, false
	}
	var delta string
	if len(focused.Delta) > 0 && json.Unmarshal(focused.Delta, &delta) == nil && delta != "" {
		return focused.Delta, true
	}
	return focused.Arguments, true
}

// captureResponsesArgumentsDoneRaw captures an arguments.done event's
// authoritative string token without decoding it through a Go string first.
func captureResponsesArgumentsDoneRaw(eventData []byte) (json.RawMessage, bool) {
	var focused struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(eventData, &focused); err != nil {
		return nil, false
	}
	return focused.Arguments, true
}

// captureResponsesOutputItemRaw extracts the raw item token from an
// output_item.done event, accepting both documented field spellings used by
// the decoded fallback above. The token remains aligned with the accumulated
// decoded item so terminal-empty settlement can preserve argument bytes.
func captureResponsesOutputItemRaw(eventData []byte) json.RawMessage {
	var focused struct {
		Item       json.RawMessage `json:"item"`
		OutputItem json.RawMessage `json:"output_item"`
	}
	if err := json.Unmarshal(eventData, &focused); err != nil {
		return nil
	}
	if len(focused.Item) > 0 {
		return focused.Item
	}
	return focused.OutputItem
}

// extractResponseObjectRaw extracts the raw bytes of the "response" object
// from a response.completed SSE event. When the payload nests the response
// under "response", that sub-object's raw JSON is returned; when the
// payload IS the response, the full event data is returned. Returns nil
// when extraction fails — the caller falls back to the lossy map path.
func extractResponseObjectRaw(eventData []byte) []byte {
	var focused struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(eventData, &focused); err != nil {
		return nil
	}
	if len(focused.Response) > 0 {
		return focused.Response
	}
	// No "response" field — the payload itself is the response object.
	return eventData
}
