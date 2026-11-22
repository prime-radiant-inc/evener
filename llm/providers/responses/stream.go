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
	id, itemID, name string
	// issuedCallID is the provider's own call_id for this call. A call the stream
	// named only by item ID leaves it empty, so settlement still matches it by item ID.
	issuedCallID string
	hasIndex     bool
	started      bool
	args         bytes.Buffer
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
	toolStates map[string]*responsesToolState
	toolIndex  map[int]*responsesToolState
	entries    []responsesAccumEntry
	// reasoningTextSeen records that some reasoning item has already emitted text, so a
	// later item's first part still needs the blank line the live preview renders.
	reasoningTextSeen bool
}

type responsesAccumPart struct {
	index    int
	hasIndex bool
	data     []byte
}

type responsesAccumEntry struct {
	index        int    // output_index, or -1 until a frame supplies it
	id           string // the wire item id, which identifies a slot that carries no output_index
	issuedCallID string // the provider-issued call_id behind a recovered call, empty when synthesized
	item         map[string]any
	parts        []responsesAccumPart
	raw          json.RawMessage
	settled      bool
	summaries    bool // reasoning deltas were summaries, which replay as summaries, never as content
	separated    bool // the live preview already emitted a separator in front of this item
}

func (e *responsesAccumEntry) hasPartContent() bool {
	return slices.ContainsFunc(e.parts, func(p responsesAccumPart) bool { return len(p.data) > 0 })
}

func (e *responsesAccumEntry) orderedParts() [][]byte {
	ordered := e.parts
	if !slices.ContainsFunc(e.parts, func(p responsesAccumPart) bool { return !p.hasIndex }) {
		ordered = slices.Clone(e.parts)
		slices.SortStableFunc(ordered, func(a, b responsesAccumPart) int { return cmp.Compare(a.index, b.index) })
	}
	out := make([][]byte, len(ordered))
	for i, p := range ordered {
		out[i] = p.data
	}
	return out
}

func (e *responsesAccumEntry) joinedParts() []byte {
	return bytes.Join(e.orderedParts(), []byte(reasoningPartSeparator))
}

func (e *responsesAccumEntry) ensurePartSlot(idx int) (slot int, created bool) {
	if slot := slices.IndexFunc(e.parts, func(p responsesAccumPart) bool { return p.hasIndex && p.index == idx }); slot >= 0 {
		return slot, false
	}
	if len(e.parts) == 1 && !e.parts[0].hasIndex && len(e.parts[0].data) == 0 {
		e.parts[0].index, e.parts[0].hasIndex = idx, true
		return 0, true
	}
	e.parts = append(e.parts, responsesAccumPart{index: idx, hasIndex: true})
	return len(e.parts) - 1, true
}

func (e *responsesAccumEntry) settleReasoning(item map[string]any) {
	field, kind := "content", "reasoning_text"
	if e.summaries {
		field, kind = "summary", "summary_text"
	}
	parts := []any{}
	for _, p := range e.orderedParts() {
		if len(p) > 0 {
			parts = append(parts, map[string]any{"type": kind, "text": string(p)})
		}
	}
	item[field] = parts
}
func newResponsesOutputAccumulator() *responsesOutputAccumulator {
	return &responsesOutputAccumulator{
		toolStates: map[string]*responsesToolState{},
		toolIndex:  map[int]*responsesToolState{},
	}
}

func (acc *responsesOutputAccumulator) settledCount() int {
	if acc == nil {
		return 0
	}
	n := 0
	for _, e := range acc.entries {
		if e.settled {
			n++
		}
	}
	return n
}

func terminalReasoningItem(terminal []any, id string) map[string]any {
	if id == "" {
		return nil
	}
	for _, raw := range terminal {
		item, _ := raw.(map[string]any)
		if kind, _ := item["type"].(string); kind == "reasoning" && item["id"] == id {
			return item
		}
	}
	return nil
}

func (acc *responsesOutputAccumulator) recoveredOutput(terminalOutput []any) (out []any, raw []json.RawMessage) {
	if !slices.ContainsFunc(acc.entries, func(e responsesAccumEntry) bool { // an ignored item carries no index
		kind, _ := e.item["type"].(string)
		return e.index < 0 && slices.Contains([]string{"message", "reasoning", "function_call", "web_search_call"}, kind)
	}) {
		slices.SortStableFunc(acc.entries, func(a, b responsesAccumEntry) int { return cmp.Compare(a.index, b.index) })
	}
	for _, e := range acc.entries {
		if e.id != "" && e.item["id"] == nil {
			e.item["id"] = e.id // the wire id the deltas carried, so a terminal copy matches by it
		}
		if e.settled {
			if e.item["type"] == "reasoning" {
				status, _ := e.item["status"].(string)
				if status == "incomplete" || status == "in_progress" {
					id, _ := e.item["id"].(string)
					if term := terminalReasoningItem(terminalOutput, id); term != nil && term["status"] == "completed" {
						e.item["status"] = "completed"
					}
				}
			}
		} else if e.item["type"] == "reasoning" {
			id, _ := e.item["id"].(string)
			term := terminalReasoningItem(terminalOutput, id)
			termStatus, _ := term["status"].(string)
			if term != nil && termStatus != "incomplete" && termStatus != "in_progress" {
				// Let the matching terminal item supply the completed summary cleanly.
				e.item["status"] = "completed"
				termSummary, _ := term["summary"].([]any)
				if len(termSummary) > 0 {
					if !e.summaries {
						e.settleReasoning(e.item)
					}
					e.item["summary"] = termSummary
				} else {
					e.settleReasoning(e.item)
				}
			} else {
				e.item["status"] = "incomplete"
				e.settleReasoning(e.item)
			}
		} else {
			e.item["content"] = []any{map[string]any{"type": "output_text", "text": string(e.joinedParts())}}
		}
		out, raw = append(out, e.item), append(raw, e.raw)
	}
	return out, raw
}

func (acc *responsesOutputAccumulator) pending(payload map[string]any, kind string) (*responsesAccumEntry, bool) {
	item, _ := cmp.Or(payload["item"], payload["output_item"]).(map[string]any)
	id1, _ := payload["item_id"].(string)
	id2, _ := item["id"].(string)
	index, id := -1, cmp.Or(id1, id2)
	if v := payload["output_index"]; v != nil {
		index = llm.IntFromAny(v)
	}
	for i := len(acc.entries) - 1; i >= 0; i-- {
		e := &acc.entries[i]
		if !e.settled && e.item["type"] == kind && (index >= 0 && e.index == index || id != "" && e.id == id || e.index < 0 && e.id == "" || index < 0 && id == "") {
			e.index, e.id = max(e.index, index), cmp.Or(e.id, id)
			return e, false
		}
	}
	acc.entries = append(acc.entries, responsesAccumEntry{index: index, id: id, item: map[string]any{"type": kind}, parts: []responsesAccumPart{{}}})
	return &acc.entries[len(acc.entries)-1], true
}

func responsesDeltaText(payload map[string]any) string {
	delta, _ := payload["delta"].(string)
	text, _ := payload["text"].(string)
	return cmp.Or(delta, text)
}

func responsesReasoningPartIndex(payload map[string]any, summary bool) (int, bool) {
	key := "content_index"
	if summary {
		key = "summary_index"
	}
	if v := payload[key]; v != nil {
		if idx := llm.IntFromAny(v); idx >= 0 {
			return idx, true
		}
	}
	return 0, false
}

func (acc *responsesOutputAccumulator) HandleDelta(payload map[string]any, kind string, summary bool) {
	delta := responsesDeltaText(payload)
	if delta == "" {
		return
	}
	entry, _ := acc.pending(payload, kind)
	slot := len(entry.parts) - 1
	if kind == "reasoning" {
		acc.reasoningTextSeen = true
		if idx, ok := responsesReasoningPartIndex(payload, summary); ok {
			slot, _ = entry.ensurePartSlot(idx)
		}
	}
	entry.parts[slot].data = append(entry.parts[slot].data, delta...)
	entry.summaries = entry.summaries || summary
}

func (acc *responsesOutputAccumulator) HandleReasoningPart(payload map[string]any, summary bool) bool {
	entry, fresh := acc.pending(payload, "reasoning")
	hasContent := entry.hasPartContent()
	created := false
	if idx, ok := responsesReasoningPartIndex(payload, summary); ok {
		_, created = entry.ensurePartSlot(idx)
	} else if hasContent {
		entry.parts = append(entry.parts, responsesAccumPart{})
		created = true
	}
	// A part boundary inside an item that already has text separates it. A newly begun
	// reasoning item has no text of its own, but the live preview is one running
	// thought, so text an earlier item emitted still needs separating -- once per item.
	if (created && hasContent) || (fresh && acc.reasoningTextSeen && !entry.separated) {
		entry.separated = true
		return true
	}
	return false
}

// responsesToolRef is how one event names a function call: the provider's
// call_id, the wire item ID, a bare payload id, and the output index.
type responsesToolRef struct {
	callID, itemID, id string
	index              int
	hasIndex           bool
}

func toolRefFromPayload(payload map[string]any) responsesToolRef {
	ref := responsesToolRef{}
	ref.callID, _ = payload["call_id"].(string)
	ref.itemID, _ = payload["item_id"].(string)
	ref.id, _ = payload["id"].(string)
	if v := payload["output_index"]; v != nil {
		ref.index, ref.hasIndex = llm.IntFromAny(v), true
	}
	return ref
}

// assignItem records a call's wire item ID. An item ID two issued calls share
// stops selecting either one, so only output_index can disambiguate it then.
func (acc *responsesOutputAccumulator) assignItem(st *responsesToolState, itemID string) {
	if itemID == "" {
		return
	}
	st.itemID = itemID
	if prev, ok := acc.toolStates[itemID]; ok {
		if prev == nil {
			return
		}
		if prev != st && prev.id != itemID && st.id != itemID {
			acc.toolStates[itemID] = nil
			return
		}
	}
	acc.toolStates[itemID] = st
}

func (acc *responsesOutputAccumulator) assignIndex(st *responsesToolState, index int, hasIndex bool) {
	if !hasIndex || st.hasIndex {
		return
	}
	st.hasIndex = true
	acc.toolIndex[index] = st
}

// names reports whether st is the call this reference names. A reference
// without a call_id names any call its item ID or index identifies.
func (ref responsesToolRef) names(st *responsesToolState) bool {
	return st != nil && (ref.callID == "" || st.id == ref.callID || st.issuedCallID == ref.callID || st.id == ref.itemID)
}

func (acc *responsesOutputAccumulator) findToolState(ref responsesToolRef, fallback bool) *responsesToolState {
	if st := acc.toolStates[ref.callID]; ref.callID != "" && st != nil {
		return st
	}
	if ref.hasIndex {
		if st := acc.toolIndex[ref.index]; ref.names(st) {
			return st
		}
	}
	if st := acc.toolStates[ref.itemID]; ref.itemID != "" && st != nil && (fallback || ref.names(st)) {
		return st
	}
	if ref.id != "" {
		return acc.toolStates[ref.id]
	}
	return nil
}

func (acc *responsesOutputAccumulator) toolStateFor(ref responsesToolRef, fallback bool, name string) *responsesToolState {
	if st := acc.findToolState(ref, fallback); st != nil {
		return st
	}
	id := cmp.Or(ref.callID, ref.itemID, ref.id)
	if id == "" {
		return nil
	}
	st := &responsesToolState{id: id, name: name}
	acc.toolStates[id] = st
	if ref.callID == "" && ref.itemID != "" {
		acc.assignItem(st, ref.itemID)
	}
	return st
}

// noteIssuedCallID records a call_id the provider itself sent for this call, including a
// done item that supplies it for a call the stream opened by item ID alone. A foreign
// call_id addressed to a different state is not this call's to record.
func (st *responsesToolState) noteIssuedCallID(callID, itemID string) {
	if callID == "" {
		return
	}
	if callID == st.id || (st.issuedCallID == "" && st.id == itemID) {
		st.issuedCallID = callID
	}
}

// HandleOutputItemAdded pre-registers a function_call tool state from a
// response.output_item.added event's item, keyed by whichever of
// call_id/item_id are present. Non-function_call items are a no-op (the
// wire shape carries no other item type worth pre-registering here).
func (acc *responsesOutputAccumulator) HandleOutputItemAdded(payload, item map[string]any) {
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		if phase, _ := item["phase"].(string); itemType == "message" && phase != "" {
			entry, _ := acc.pending(payload, "message")
			entry.item["phase"] = phase
		}
		return
	}
	callID, _ := item["call_id"].(string)
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
	if callID == "" && itemID == "" {
		return
	}
	ref := responsesToolRef{callID: callID, itemID: itemID}
	if v := payload["output_index"]; v != nil {
		ref.index, ref.hasIndex = llm.IntFromAny(v), true
	}
	st := acc.toolStateFor(ref, false, name)
	if st == nil {
		return
	}
	if callID != "" {
		st.id = callID
		st.noteIssuedCallID(callID, itemID)
	}
	acc.assignItem(st, itemID)
	acc.assignIndex(st, ref.index, ref.hasIndex)
	if st.name == "" && name != "" {
		st.name = name
	}
	acc.toolStates[st.id] = st
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
	ref := toolRefFromPayload(payload)
	name, _ := payload["name"].(string)
	if (ref.callID == "" && ref.itemID == "" && ref.id == "" && !ref.hasIndex) || (delta == "" && name == "") {
		return nil, "", false
	}
	// A delta cannot wait for a later record: emit it against the item's
	// current owner, as the provider's references have always fallen back.
	st := acc.toolStateFor(ref, true, name)
	if st == nil {
		return nil, "", false
	}
	st.noteIssuedCallID(ref.callID, ref.itemID)
	if st.name == "" && name != "" {
		st.name = name
	}
	if delta != "" {
		fragment := []byte(delta)
		if rawDelta, rawOK := captureResponsesDeltaRaw(eventData); rawOK && rawDelta != nil {
			if content, err := protocolhttp.RawStringContent(rawDelta); err == nil && len(content) > 0 {
				fragment = content
			}
		}
		st.args.Write(fragment)
		delta = string(fragment)
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
	ref := toolRefFromPayload(payload)
	name, _ := payload["name"].(string)
	if ref.callID == "" && ref.itemID == "" && ref.id == "" {
		return nil, false
	}
	// A finalized frame naming a call whose item belongs to another call starts
	// that call instead of overwriting the item's owner.
	st := acc.toolStateFor(ref, false, name)
	if st == nil {
		return nil, false
	}
	st.noteIssuedCallID(ref.callID, ref.itemID)
	if st.name == "" && name != "" {
		st.name = name
	}
	if argsStr != "" {
		st.args.Reset()
		if rawArgs, rawOK := captureResponsesArgumentsDoneRaw(eventData); rawOK && rawArgs != nil {
			if content, err := protocolhttp.RawStringContent(rawArgs); err == nil {
				st.args.Write(content)
				return st, true
			}
		}
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
// appended to Output()) and "unfinished function_call or no resolvable call_id"
// (rawItem is non-nil, not appended) -- callers distinguish the two by
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
	it, _ := item["type"].(string)
	index := -1
	if v := payload["output_index"]; v != nil {
		index = llm.IntFromAny(v)
	}
	if it != "function_call" {
		entry, _ := acc.pending(payload, it)
		if it == "message" && !slices.ContainsFunc(responsesReasoningParts(item["content"]), func(p llm.ResponsesReasoningPart) bool { return p.Text != "" || p.Type != "output_text" }) {
			item["content"] = []any{map[string]any{"type": "output_text", "text": string(entry.joinedParts())}}
		}
		if phase, _ := item["phase"].(string); it == "message" && phase == "" && entry.item["phase"] != nil {
			item["phase"] = entry.item["phase"]
		}
		if it == "reasoning" && entry.hasPartContent() && !slices.ContainsFunc(append(responsesReasoningParts(item["content"]), responsesReasoningParts(item["summary"])...), func(p llm.ResponsesReasoningPart) bool { return p.Text != "" }) {
			entry.settleReasoning(item)
		}
		if entry.id != "" && item["id"] == nil {
			item["id"] = entry.id // the deltas named this item before it settled
		}
		entry.raw, entry.item, entry.settled = rawItemToken, item, true
		return item, nil, "", false
	}
	if responsesFunctionCallUnfinished(item) {
		return item, nil, "", false
	}
	issuedCallID, _ := item["call_id"].(string)
	callID := issuedCallID
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
	argsStr, _ = item["arguments"].(string)
	if rawArgs, rawOK := captureResponsesItemArgsRaw(rawItemToken); rawOK && rawArgs != nil {
		if content, err := protocolhttp.RawStringContent(rawArgs); err == nil {
			argsStr = string(content)
		}
	}
	if callID == "" && itemID != "" {
		if st := acc.toolStates[itemID]; st != nil {
			callID = st.id
		}
	}
	if callID == "" {
		return item, nil, "", false
	}
	st := acc.findToolState(responsesToolRef{callID: callID, itemID: itemID, index: index, hasIndex: index >= 0}, false)
	if st == nil {
		st = &responsesToolState{id: callID}
	}
	if issuedCallID != "" {
		st.noteIssuedCallID(issuedCallID, itemID)
	}
	acc.assignItem(st, itemID)
	acc.assignIndex(st, index, index >= 0)
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
	acc.toolStates[st.id] = st
	acc.entries = append(acc.entries, responsesAccumEntry{index: index, settled: true, raw: rawItemToken,
		issuedCallID: st.issuedCallID,
		item:         map[string]any{"type": "function_call", "call_id": st.id, "id": st.itemID, "name": st.name, "arguments": argsStr}})
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
	"response.incomplete":                    {},
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
			case errors.Is(parseErr, context.DeadlineExceeded):
				return llm.WrapContextError(res.Instance, parseErr)
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
				// cleanly without a terminal response — a truncated response,
				// with no read error to report as its cause.
				return llm.NewStreamError(res.Instance, "responses stream closed before a terminal response", nil)
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
				delta := responsesDeltaText(payload)
				if delta == "" {
					return nil
				}
				acc.HandleDelta(payload, "message", false)
				if !textStarted {
					textStarted = true
					s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: textID})
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: textID, Delta: delta})
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				// OpenAI streams reasoning as summaries; some gateways
				// (lunaroute fronting GLM) stream the raw reasoning text
				// instead. Both are the model thinking, so both render as
				// reasoning.
				delta := responsesDeltaText(payload)
				if delta == "" {
					return nil
				}
				acc.HandleDelta(payload, "reasoning", typ == "response.reasoning_summary_text.delta")
				s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: delta})
			case "response.reasoning_summary_part.added", "response.reasoning_part.added":
				// Detailed reasoning arrives as multiple parts; separate them
				// with a blank line so the rendered thought stays readable.
				if acc.HandleReasoningPart(payload, typ == "response.reasoning_summary_part.added") {
					s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: reasoningPartSeparator})
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
					args, spelling := llm.NormalizeToolArguments([]byte(argsStr))
					tc := llm.ToolCallData{ID: st.id, ItemID: st.itemID, Name: st.name, Arguments: args, ArgumentsSpelling: spelling, Type: "function"}
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
				case rawItem == nil:
					// Best-effort: treat as end-of-text.
					if textStarted {
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
						textStarted = false
					}
				default:
					if it, _ := rawItem["type"].(string); it == "function_call" {
						// Unfinished function_call or no resolvable call_id.
						s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload, ContentBearing: true})
					} else if textStarted {
						// Best-effort: treat as end-of-text.
						s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: textID})
						textStarted = false
					}
				}
			case "response.completed", "response.incomplete":
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
				built := fromResponses(rawResp, req.Model, respBody)
				settleResponsesTerminalOutput(&built, rawResp, acc)
				built.Provider = res.Instance
				llm.StampContinuationIDHash(sctx, &built, p.Hasher)
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
