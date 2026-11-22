package google

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// maxInteractionStreamEventBytes bounds one Interactions SSE event. Real events
// carry a step or a whole interaction and stay far below this; the bound only
// stops a malformed or hostile stream from buffering without limit.
const maxInteractionStreamEventBytes = 8 << 20

type interactionStreamSlot struct {
	index          int
	stepType       string
	stepMap        map[string]any
	spanStarted    bool
	spanEnded      bool
	deferredEvents []llm.StreamEvent

	// thought
	signature          string
	summaryItems       []string
	hadStreamedSummary bool

	// model_output
	textID              string
	textBuilders        []*strings.Builder
	textNeedsNewBuilder bool
	hadStreamedText     bool
	citations           []llm.WebSearchSource
	stepError           map[string]any
	nonTextModalities   []string

	// function_call
	callID                 string
	callName               string
	initialArgs            []byte
	hasNonEmptyInitialArgs bool
	argBuf                 []byte
	receivedArgFragments   bool
	settledArgs            json.RawMessage
	settledSpelling        llm.Spelling
	hasSettledArgs         bool
	backfilledArgBytes     []byte
}

func (s *interactionStreamSlot) startToolCall(instance, wireModel string) *llm.ToolCallData {
	return &llm.ToolCallData{
		GoogleInteraction: true,
		GoogleInstance:    instance,
		GoogleModel:       wireModel,
		ID:                s.callID,
		Name:              s.callName,
		Type:              "function",
		ThoughtSignature:  s.signature,
	}
}

func (s *interactionStreamSlot) settleStreamedOrInitialArgs() bool {
	if !s.receivedArgFragments && !s.hasNonEmptyInitialArgs {
		return false
	}
	rawBytes := s.initialArgs
	if s.receivedArgFragments {
		rawBytes = s.argBuf
	}
	s.settledArgs, s.settledSpelling = llm.NormalizeToolArguments(rawBytes)
	s.hasSettledArgs = true
	return true
}

func (s *interactionStreamSlot) nonEmptyTextItems() []string {
	var items []string
	for _, b := range s.textBuilders {
		if txt := b.String(); txt != "" {
			items = append(items, txt)
		}
	}
	return items
}

func (s *interactionStreamSlot) settledToolCall(instance, wireModel string) llm.ToolCallData {
	args := s.settledArgs
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return llm.ToolCallData{
		GoogleInteraction: true,
		GoogleInstance:    instance,
		GoogleModel:       wireModel,
		ID:                s.callID,
		Name:              s.callName,
		Type:              "function",
		Arguments:         args,
		ThoughtSignature:  s.signature,
		ArgumentsSpelling: s.settledSpelling,
	}
}

type interactionStreamAccumulator struct {
	stream            *llm.ChanStream
	instance          string
	wireModel         string
	requestedModel    string
	summariesDisabled bool

	id                string
	model             string
	status            string
	continuationToken string
	latestUsage       map[string]any

	slots         map[int]*interactionStreamSlot
	nextEmitIndex int
}

func newInteractionStreamAccumulator(stream *llm.ChanStream, instance, wireModel, requestedModel string, summariesDisabled bool) *interactionStreamAccumulator {
	return &interactionStreamAccumulator{
		stream:            stream,
		instance:          instance,
		wireModel:         wireModel,
		requestedModel:    requestedModel,
		summariesDisabled: summariesDisabled,
		slots:             map[int]*interactionStreamSlot{},
	}
}

func (a *interactionStreamAccumulator) ensureSlot(idx int, stepType string) *interactionStreamSlot {
	slot, ok := a.slots[idx]
	if !ok {
		slot = &interactionStreamSlot{
			index:    idx,
			stepType: stepType,
			textID:   fmt.Sprintf("text_%d", idx),
		}
		a.slots[idx] = slot
	} else if slot.stepType == "" && stepType != "" {
		slot.stepType = stepType
	}
	return slot
}

func (a *interactionStreamAccumulator) send(ev llm.StreamEvent) {
	if a.stream != nil {
		a.stream.Send(ev)
	}
}

func (a *interactionStreamAccumulator) flushDeferred(slot *interactionStreamSlot) {
	for _, ev := range slot.deferredEvents {
		a.send(ev)
	}
	slot.deferredEvents = nil
}

func (a *interactionStreamAccumulator) emit(slot *interactionStreamSlot, ev llm.StreamEvent) {
	if a.stream == nil {
		return
	}
	if slot.index == a.nextEmitIndex {
		a.flushDeferred(slot)
		a.send(ev)
		return
	}
	if slot.index > a.nextEmitIndex {
		slot.deferredEvents = append(slot.deferredEvents, ev)
		return
	}
	a.send(ev)
}

func (a *interactionStreamAccumulator) advanceEmitIndex() {
	for {
		slot, ok := a.slots[a.nextEmitIndex]
		if !ok || !slot.spanEnded {
			return
		}
		a.nextEmitIndex++
		if next, hasNext := a.slots[a.nextEmitIndex]; hasNext && len(next.deferredEvents) > 0 {
			a.flushDeferred(next)
		}
	}
}

func (a *interactionStreamAccumulator) handleLifecycle(eventType string, payload map[string]any) {
	switch eventType {
	case "interaction.created":
		inter, _ := payload["interaction"].(map[string]any)
		if inter == nil {
			inter = payload
		}
		a.id = cmp.Or(stringVal(inter["id"]), stringVal(payload["interaction_id"]), a.id)
		a.model = cmp.Or(stringVal(inter["model"]), a.model)
		a.status = cmp.Or(stringVal(inter["status"]), a.status)
		if tok := stringVal(inter["continuation_token"]); tok != "" {
			a.continuationToken = tok
		}
		if u, ok := inter["usage"].(map[string]any); ok && len(u) > 0 {
			a.latestUsage = u
		}
	case "interaction.status_update":
		a.id = cmp.Or(stringVal(payload["interaction_id"]), a.id)
		a.status = cmp.Or(stringVal(payload["status"]), a.status)
		if tok := stringVal(payload["continuation_token"]); tok != "" {
			a.continuationToken = tok
		}
		if u, ok := payload["usage"].(map[string]any); ok && len(u) > 0 {
			a.latestUsage = u
		}
	}
}

func (a *interactionStreamAccumulator) handleStepStart(idx int, step map[string]any, evData []byte) {
	stepType := stringVal(step["type"])
	slot := a.ensureSlot(idx, stepType)
	switch stepType {
	case "thought":
		if sig := strings.TrimSpace(stringVal(step["signature"])); sig != "" {
			slot.signature = sig
		}
		if !a.summariesDisabled {
			items := extractThoughtSummaryItems(step["summary"])
			if len(items) > 0 {
				slot.summaryItems = append(slot.summaryItems, items...)
				slot.hadStreamedSummary = true
				if !slot.spanStarted {
					slot.spanStarted = true
					a.emit(slot, llm.StreamEvent{Type: llm.StreamEventReasoningStart})
				}
				a.emit(slot, llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: strings.Join(items, "\n\n")})
			}
		}
	case "model_output":
		if errMap, ok := step["error"].(map[string]any); ok {
			slot.stepError = errMap
		}
		if content, ok := step["content"].([]any); ok {
			for _, itemAny := range content {
				item, ok := itemAny.(map[string]any)
				if !ok {
					continue
				}
				itemType := stringVal(item["type"])
				switch itemType {
				case "text":
					if ann := item["annotations"]; ann != nil {
						slot.citations = appendUniqueSources(slot.citations, extractCitationAnnotations(ann))
					}
					if t := stringVal(item["text"]); t != "" {
						b := new(strings.Builder)
						b.WriteString(t)
						slot.textBuilders = append(slot.textBuilders, b)
						slot.textNeedsNewBuilder = false
						slot.hadStreamedText = true
						if !slot.spanStarted {
							slot.spanStarted = true
							a.emit(slot, llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: slot.textID})
						}
						a.emit(slot, llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: slot.textID, Delta: t})
					}
				case "image", "audio", "video", "document":
					slot.nonTextModalities = appendUniqueStrings(slot.nonTextModalities, itemType)
					slot.textNeedsNewBuilder = true
				}
			}
		}
	case "function_call":
		slot.callID = cmp.Or(stringVal(step["id"]), slot.callID)
		if slot.callID == "" {
			slot.callID = identifier.MustNewSyntheticCallID()
		}
		slot.callName = cmp.Or(stringVal(step["name"]), slot.callName)
		if sig := strings.TrimSpace(stringVal(step["signature"])); sig != "" {
			slot.signature = sig
		}
		if rawTok := captureStreamStepArgsRaw(evData); len(rawTok) > 0 && !bytes.Equal(rawTok, []byte("null")) {
			if rawTok[0] == '"' {
				if unescaped, err := protocolhttp.RawStringContent(rawTok); err == nil {
					trimmed := bytes.TrimSpace(unescaped)
					if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("{}")) {
						slot.initialArgs = unescaped
						slot.hasNonEmptyInitialArgs = true
					}
				}
			} else if m, ok := step["arguments"].(map[string]any); ok && len(m) > 0 {
				slot.initialArgs = append([]byte(nil), rawTok...)
				slot.hasNonEmptyInitialArgs = true
			}
		} else if argsAny := step["arguments"]; argsAny != nil {
			switch v := argsAny.(type) {
			case map[string]any:
				if len(v) > 0 {
					if b, err := json.Marshal(normalizeJSONNumbers(v)); err == nil {
						slot.initialArgs = b
						slot.hasNonEmptyInitialArgs = true
					}
				}
			case string:
				if trimmed := strings.TrimSpace(v); trimmed != "" && trimmed != "{}" {
					slot.initialArgs = []byte(v)
					slot.hasNonEmptyInitialArgs = true
				}
			}
		}
		if !slot.spanStarted {
			slot.spanStarted = true
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: slot.startToolCall(a.instance, a.wireModel)})
		}
	case "google_search_call":
		slot.stepMap = maps.Clone(step)
	case "google_search_result":
		slot.stepMap = normalizeGoogleSearchResultStep(step)
	}
}

func (a *interactionStreamAccumulator) handleStepDelta(idx int, delta map[string]any, evData []byte) {
	deltaType := stringVal(delta["type"])
	slot := a.ensureSlot(idx, "")
	switch deltaType {
	case "thought_summary":
		if a.summariesDisabled {
			return
		}
		var itemText string
		if c, ok := delta["content"].(map[string]any); ok {
			itemText = stringVal(c["text"])
		}
		if itemText == "" {
			itemText = stringVal(delta["text"])
		}
		if itemText == "" {
			return
		}
		if !slot.spanStarted {
			slot.spanStarted = true
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventReasoningStart})
		}
		emitDelta := itemText
		if len(slot.summaryItems) > 0 {
			emitDelta = "\n\n" + itemText
		}
		slot.summaryItems = append(slot.summaryItems, itemText)
		slot.hadStreamedSummary = true
		a.emit(slot, llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: emitDelta})
	case "thought_signature":
		if sig := strings.TrimSpace(stringVal(delta["signature"])); sig != "" {
			slot.signature = sig
		}
	case "text":
		chunk := stringVal(delta["text"])
		if chunk == "" {
			return
		}
		if !slot.spanStarted {
			slot.spanStarted = true
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: slot.textID})
		}
		if len(slot.textBuilders) == 0 || slot.textNeedsNewBuilder {
			slot.textBuilders = append(slot.textBuilders, new(strings.Builder))
			slot.textNeedsNewBuilder = false
		}
		slot.textBuilders[len(slot.textBuilders)-1].WriteString(chunk)
		slot.hadStreamedText = true
		a.emit(slot, llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: slot.textID, Delta: chunk})
	case "text_annotation_delta":
		slot.citations = appendUniqueSources(slot.citations, extractCitationAnnotations(delta))
	case "image", "audio", "video", "document":
		slot.nonTextModalities = appendUniqueStrings(slot.nonTextModalities, deltaType)
		slot.textNeedsNewBuilder = true
	case "arguments_delta":
		frag := extractDeltaArgFragment(delta, evData)
		if len(frag) == 0 {
			break
		}
		slot.receivedArgFragments = true
		slot.argBuf = append(slot.argBuf, frag...)
		if !slot.spanStarted {
			slot.spanStarted = true
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: slot.startToolCall(a.instance, a.wireModel)})
		}
		a.emit(slot, llm.StreamEvent{
			Type: llm.StreamEventToolCallDelta,
			ToolCall: &llm.ToolCallData{
				ID:        slot.callID,
				Name:      slot.callName,
				Type:      "function",
				Arguments: append(json.RawMessage(nil), frag...),
			},
		})
	case "google_search_call":
		if slot.stepMap == nil {
			slot.stepMap = map[string]any{"type": "google_search_call"}
		}
		if args, ok := delta["arguments"].(map[string]any); ok {
			slot.stepMap["arguments"] = args
		}
		if sig := stringVal(delta["signature"]); sig != "" {
			slot.stepMap["signature"] = sig
		}
	case "google_search_result":
		if slot.stepMap == nil {
			slot.stepMap = map[string]any{"type": "google_search_result"}
		}
		mergeSearchResultDelta(slot.stepMap, delta)
	}
}

func mergeSearchResultDelta(dst, delta map[string]any) {
	if cid := stringVal(delta["call_id"]); cid != "" && stringVal(dst["call_id"]) == "" {
		dst["call_id"] = cid
	}
	if st := stringVal(delta["search_type"]); st != "" {
		dst["search_type"] = st
	}
	if ie, has := delta["is_error"]; has {
		dst["is_error"] = ie
	}
	if sig := stringVal(delta["signature"]); sig != "" {
		dst["signature"] = sig
	}
	// Merge delta["result"] onto existing dst["result"] without dropping existing items.
	var existing []any
	switch cur := dst["result"].(type) {
	case []any:
		existing = cur
	case map[string]any:
		existing = []any{cur}
	}
	if resVal, has := delta["result"]; has && resVal != nil {
		switch add := resVal.(type) {
		case []any:
			existing = append(existing, add...)
		case map[string]any:
			existing = append(existing, add)
		}
		dst["result"] = existing
	}
	tmp := maps.Clone(dst)
	if ss, has := delta["search_suggestions"]; has && ss != nil {
		tmp["search_suggestions"] = ss
	}
	if rc, has := delta["rendered_content"]; has && rc != nil {
		tmp["rendered_content"] = rc
	}
	norm := normalizeGoogleSearchResultStep(tmp)
	if normRes, ok := norm["result"]; ok {
		dst["result"] = normRes
	}
}

func (a *interactionStreamAccumulator) handleStepStop(idx int, payload map[string]any) {
	if u, ok := payload["usage"].(map[string]any); ok && len(u) > 0 {
		a.latestUsage = u
	}
	slot, ok := a.slots[idx]
	if !ok {
		return
	}
	switch slot.stepType {
	case "thought":
		if slot.hadStreamedSummary && slot.spanStarted {
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
		}
		slot.spanEnded = true
		a.advanceEmitIndex()
	case "model_output":
		if slot.hadStreamedText {
			if slot.spanStarted {
				a.emit(slot, llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: slot.textID})
			}
			slot.spanEnded = true
			a.advanceEmitIndex()
		}
	case "function_call":
		if slot.settleStreamedOrInitialArgs() {
			tc := slot.settledToolCall(a.instance, a.wireModel)
			a.emit(slot, llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
			slot.spanEnded = true
			a.advanceEmitIndex()
		}
	case "google_search_call", "google_search_result":
		slot.spanEnded = true
		a.advanceEmitIndex()
	}
}

func (a *interactionStreamAccumulator) complete(payload map[string]any, evData []byte) (llm.Response, error) {
	inter, _ := payload["interaction"].(map[string]any)
	if inter == nil {
		inter = payload
	}
	interCopy := maps.Clone(inter)
	if interCopy == nil {
		interCopy = map[string]any{}
	}
	a.id = cmp.Or(stringVal(interCopy["id"]), a.id)
	a.model = cmp.Or(stringVal(interCopy["model"]), a.model, a.requestedModel)
	a.status = cmp.Or(stringVal(interCopy["status"]), a.status)
	if tok := stringVal(interCopy["continuation_token"]); tok != "" {
		a.continuationToken = tok
	} else if a.continuationToken != "" {
		interCopy["continuation_token"] = a.continuationToken
	}

	wireModel := a.wireModel
	if a.instance == "" && wireModel == "" {
		wireModel = a.model
	}

	var rawTermArgs []json.RawMessage
	var rawTermCaptured bool
	termSteps, _ := interCopy["steps"].([]any)
	for stepIdx, rawStep := range termSteps {
		stepMap, ok := rawStep.(map[string]any)
		if !ok {
			continue
		}
		stepType := stringVal(stepMap["type"])
		slot := a.ensureSlot(stepIdx, stepType)
		switch slot.stepType {
		case "thought":
			if slot.signature == "" {
				slot.signature = strings.TrimSpace(stringVal(stepMap["signature"]))
			}
			if !a.summariesDisabled && len(slot.summaryItems) == 0 {
				slot.summaryItems = extractThoughtSummaryItems(stepMap["summary"])
			}
		case "model_output":
			if slot.stepError == nil {
				if errMap, ok := stepMap["error"].(map[string]any); ok {
					slot.stepError = errMap
				}
			}
			if content, ok := stepMap["content"].([]any); ok {
				texts, citations, modalities := extractModelOutputContent(content, !slot.hadStreamedText)
				slot.citations = appendUniqueSources(slot.citations, citations)
				slot.nonTextModalities = appendUniqueStrings(slot.nonTextModalities, modalities...)
				for _, t := range texts {
					b := new(strings.Builder)
					b.WriteString(t)
					slot.textBuilders = append(slot.textBuilders, b)
				}
			}
		case "function_call":
			slot.callID = cmp.Or(slot.callID, stringVal(stepMap["id"]))
			slot.callName = cmp.Or(slot.callName, stringVal(stepMap["name"]))
			if slot.signature == "" {
				slot.signature = strings.TrimSpace(stringVal(stepMap["signature"]))
			}
			if !slot.hasSettledArgs {
				// Complete streamed JSON object wins if present on open/unresolved slot.
				if slot.receivedArgFragments {
					trimmed := bytes.TrimSpace(slot.argBuf)
					if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
						slot.settledArgs, slot.settledSpelling = llm.NormalizeToolArguments(slot.argBuf)
						slot.hasSettledArgs = true
					}
				}
				if !slot.hasSettledArgs && stepMap["arguments"] != nil {
					var rawTok json.RawMessage
					if s, isStr := stepMap["arguments"].(string); !isStr || strings.ContainsRune(s, '\ufffd') {
						if !rawTermCaptured {
							rawTermCaptured = true
							rawTermArgs = captureCompletedStepsArgsRaw(evData)
						}
						if stepIdx < len(rawTermArgs) {
							rawTok = rawTermArgs[stepIdx]
						}
					}
					termArgs, termSpelling := resolveInteractionStepArgs(rawTok, stepMap["arguments"])
					isEmptyObj := bytes.Equal(bytes.TrimSpace(termArgs), []byte("{}"))
					_, isTermStr := stepMap["arguments"].(string)
					hasLiveArgs := slot.receivedArgFragments || slot.hasNonEmptyInitialArgs
					validTermObj := len(termArgs) > 0 && termArgs[0] == '{' && json.Valid(termArgs) && (!isEmptyObj || !hasLiveArgs)
					malformedTermStr := !hasLiveArgs && isTermStr && len(bytes.TrimSpace(termArgs)) > 0 && !isEmptyObj
					if validTermObj || malformedTermStr {
						slot.settledArgs = termArgs
						slot.settledSpelling = termSpelling
						slot.hasSettledArgs = true
						tc := slot.settledToolCall(a.instance, wireModel)
						rawWire := []byte(tc.WireArguments())
						if !bytes.Equal(bytes.TrimSpace(rawWire), []byte("{}")) && !bytes.Equal(rawWire, slot.argBuf) {
							if len(slot.argBuf) == 0 {
								slot.backfilledArgBytes = rawWire
							} else if bytes.HasPrefix(rawWire, slot.argBuf) {
								slot.backfilledArgBytes = rawWire[len(slot.argBuf):]
							}
						}
					}
				}
			}
		case "google_search_call":
			if slot.stepMap == nil {
				slot.stepMap = maps.Clone(stepMap)
			} else {
				if stringVal(slot.stepMap["id"]) == "" && stringVal(stepMap["id"]) != "" {
					slot.stepMap["id"] = stepMap["id"]
				}
				if slot.stepMap["arguments"] == nil && stepMap["arguments"] != nil {
					slot.stepMap["arguments"] = stepMap["arguments"]
				}
			}
		case "google_search_result":
			norm := normalizeGoogleSearchResultStep(stepMap)
			if slot.stepMap == nil {
				slot.stepMap = norm
			} else {
				for _, k := range []string{"call_id", "search_type", "is_error", "result", "signature"} {
					if slot.stepMap[k] == nil || slot.stepMap[k] == "" {
						if v, ok := norm[k]; ok {
							slot.stepMap[k] = v
						}
					}
				}
			}
		}
	}

	indices := slices.Sorted(maps.Keys(a.slots))

	// Finalize any remaining open/unresolved function_call slots and emit remaining events in index order.
	for _, j := range indices {
		slot := a.slots[j]
		if slot.stepType == "function_call" {
			if slot.callID == "" {
				slot.callID = identifier.MustNewSyntheticCallID()
			}
			if !slot.hasSettledArgs && !slot.settleStreamedOrInitialArgs() {
				slot.settledArgs = json.RawMessage("{}")
				slot.hasSettledArgs = true
			}
		}
	}

	for _, j := range indices {
		if j < a.nextEmitIndex {
			continue
		}
		slot := a.slots[j]
		a.flushDeferred(slot)
		a.finishOpenSlot(slot, wireModel)
	}
	if len(indices) > 0 {
		if last := indices[len(indices)-1]; last >= a.nextEmitIndex {
			a.nextEmitIndex = last + 1
		}
	}

	steps := make([]interactionStep, 0, len(indices))
	for _, j := range indices {
		slot := a.slots[j]
		st := interactionStep{
			stepIdx:           slot.index,
			stepType:          slot.stepType,
			rawMap:            slot.stepMap,
			signature:         slot.signature,
			summaryItems:      slot.summaryItems,
			citations:         slot.citations,
			stepError:         slot.stepError,
			nonTextModalities: slot.nonTextModalities,
			callID:            slot.callID,
			callName:          slot.callName,
			settledArgs:       slot.settledArgs,
			settledSpelling:   slot.settledSpelling,
			textItems:         slot.nonEmptyTextItems(),
		}
		steps = append(steps, st)
	}

	resp := llm.Response{
		ID:       a.id,
		Provider: cmp.Or(strings.TrimSpace(a.instance), "google"),
		Model:    a.model,
		Message:  llm.Message{Role: llm.RoleAssistant},
		Raw:      interCopy,
	}
	stepErrMap, nonTextModalities := appendInteractionSteps(&resp.Message, steps, a.instance, wireModel)

	if u, ok := interCopy["usage"].(map[string]any); ok && len(u) > 0 {
		resp.Usage = parseInteractionUsage(u)
	} else if len(a.latestUsage) > 0 {
		resp.Usage = parseInteractionUsage(a.latestUsage)
	}

	if stringVal(interCopy["status"]) == "" && a.status != "" {
		interCopy["status"] = a.status
	}
	if err := settleInteractionStatus(&resp, interCopy, stepErrMap, nonTextModalities); err != nil {
		return llm.Response{}, err
	}
	return resp, nil
}

func (a *interactionStreamAccumulator) finishOpenSlot(slot *interactionStreamSlot, wireModel string) {
	if slot.spanEnded {
		return
	}
	switch slot.stepType {
	case "model_output":
		if !slot.hadStreamedText {
			items := slot.nonEmptyTextItems()
			if len(items) > 0 {
				if !slot.spanStarted {
					slot.spanStarted = true
					a.send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: slot.textID})
				}
				for _, item := range items {
					a.send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: slot.textID, Delta: item})
				}
				a.send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: slot.textID})
				slot.spanEnded = true
			}
		} else if slot.spanStarted {
			a.send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: slot.textID})
			slot.spanEnded = true
		}
	case "thought":
		if !a.summariesDisabled && !slot.hadStreamedSummary && len(slot.summaryItems) > 0 {
			if !slot.spanStarted {
				slot.spanStarted = true
				a.send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
			}
			a.send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: strings.Join(slot.summaryItems, "\n\n")})
			a.send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
			slot.spanEnded = true
		} else if slot.spanStarted {
			a.send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
			slot.spanEnded = true
		}
	case "function_call":
		if !slot.spanStarted {
			slot.spanStarted = true
			a.send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: slot.startToolCall(a.instance, wireModel)})
		}
		if len(slot.backfilledArgBytes) > 0 {
			a.send(llm.StreamEvent{
				Type: llm.StreamEventToolCallDelta,
				ToolCall: &llm.ToolCallData{
					ID:        slot.callID,
					Name:      slot.callName,
					Type:      "function",
					Arguments: append(json.RawMessage(nil), slot.backfilledArgBytes...),
				},
			})
		}
		tc := slot.settledToolCall(a.instance, wireModel)
		a.send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &tc})
		slot.spanEnded = true
	}
}

func decodeInteractionStream(
	sctx context.Context,
	cancel context.CancelFunc,
	resp *http.Response,
	s *llm.ChanStream,
	req llm.Request,
	instance, wireModel, endpointURL string,
	material llm.APILogCredentialMaterial,
	attempt *transport.APIAttemptCapture,
	summariesDisabled, stored bool,
	hasher *llm.ContinuationHasher,
) {
	defer func() {
		_ = resp.Body.Close()
		s.CloseSend()
	}()

	acc := newInteractionStreamAccumulator(s, instance, wireModel, req.Model, summariesDisabled)
	finished := false
	provider := cmp.Or(strings.TrimSpace(instance), "google")

	var runner *transport.StreamRunner
	runner = &transport.StreamRunner{
		Provider:      provider,
		Resp:          resp,
		Stream:        s,
		Attempt:       attempt,
		StatusCode:    resp.StatusCode,
		Cancel:        cancel,
		Finished:      &finished,
		IncompleteMsg: provider + " stream ended without completion",
		SSEOpts:       []llm.SSEOption{llm.WithMaxEventBytes(maxInteractionStreamEventBytes)},
		TerminalError: func(parseErr error) error {
			if acc.status == "queued" || (stored && acc.id != "") {
				status := cmp.Or(acc.status, "in_progress")
				return &interactionError{
					sentinel: ErrPendingInteractionStatus,
					provider: provider,
					message:  fmt.Sprintf("interaction %q stream closed while status %q", acc.id, status),
					raw:      map[string]any{"status": status, "interaction_id": acc.id},
					cause:    parseErr,
				}
			}
			if errors.Is(parseErr, context.DeadlineExceeded) || errors.Is(parseErr, llm.ErrResponseIdleTimeout) {
				return llm.WrapContextError(provider, parseErr)
			}
			if errors.Is(parseErr, llm.ErrSSEEventTooLarge) {
				// A bound violation is not a truncated stream: report it as itself.
				return llm.NewStreamError(provider, parseErr.Error(), parseErr)
			}
			return llm.NewStreamError(provider, runner.IncompleteMsg, parseErr)
		},
		OnEvent: func(ev llm.SSEEvent) error {
			if len(ev.Data) == 0 {
				return nil
			}
			if ev.Event == "done" || bytes.Equal(bytes.TrimSpace(ev.Data), []byte("[DONE]")) {
				return nil
			}
			var payload map[string]any
			dec := json.NewDecoder(bytes.NewReader(ev.Data))
			dec.UseNumber()
			if err := dec.Decode(&payload); err != nil {
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: map[string]any{"event": ev.Event, "data": string(ev.Data)}})
				return nil //nolint:nilerr // decode failure is surfaced as a raw passthrough event, not a fatal error
			}
			eventType := stringVal(payload["event_type"])
			if eventType == "" {
				eventType = ev.Event
			}
			switch eventType {
			case "interaction.created", "interaction.status_update":
				acc.handleLifecycle(eventType, payload)
			case "step.start", "step.delta", "step.stop":
				idx := llm.IntFromAny(payload["index"])
				if idx < 0 {
					return &transport.FatalStreamError{Err: llm.NewStreamError(provider, fmt.Sprintf("%s stream %s has negative step index %d", provider, eventType, idx), nil)}
				}
				switch eventType {
				case "step.start":
					if step, ok := payload["step"].(map[string]any); ok {
						acc.handleStepStart(idx, step, ev.Data)
					}
				case "step.delta":
					if delta, ok := payload["delta"].(map[string]any); ok {
						acc.handleStepDelta(idx, delta, ev.Data)
					}
				case "step.stop":
					acc.handleStepStop(idx, payload)
				}
			case "interaction.completed":
				r, err := acc.complete(payload, ev.Data)
				if err != nil {
					return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(err, provider)}
				}
				llm.StampEndpointURL(&r, llm.FinalResponseEndpointURL(resp, endpointURL), material)
				llm.StampContinuationIDHash(sctx, &r, hasher)
				runner.Finish(r)
			case "error":
				errObj, _ := payload["error"].(map[string]any)
				code := stringVal(errObj["code"])
				msg := stringVal(errObj["message"])
				errRaw := maps.Clone(payload)
				if errRaw == nil {
					errRaw = map[string]any{}
				}
				if acc.id != "" && errRaw["interaction_id"] == nil && errRaw["id"] == nil {
					errRaw["interaction_id"] = acc.id
				}
				err := classifyInteractionError(code, msg, errRaw)
				return &transport.FatalStreamError{Err: llm.RewriteErrorProvider(err, provider)}
			default:
				s.Send(llm.StreamEvent{Type: llm.StreamEventProviderEvent, Raw: payload})
			}
			return nil
		},
	}
	runner.Run(sctx)
}

func captureStreamStepArgsRaw(evData []byte) json.RawMessage {
	if len(evData) == 0 {
		return nil
	}
	var focused struct {
		Step struct {
			Arguments json.RawMessage `json:"arguments,omitempty"`
		} `json:"step"`
	}
	if err := rawArgsBodyDecode(evData, &focused); err != nil {
		return nil
	}
	return bytes.TrimSpace(focused.Step.Arguments)
}

func captureCompletedStepsArgsRaw(evData []byte) []json.RawMessage {
	if len(evData) == 0 {
		return nil
	}
	var focused struct {
		Interaction struct {
			Steps []interactionStepRaw `json:"steps"`
		} `json:"interaction"`
	}
	if err := rawArgsBodyDecode(evData, &focused); err != nil {
		return nil
	}
	return collectStepArgsRaw(focused.Interaction.Steps)
}

func extractDeltaArgFragment(delta map[string]any, evData []byte) []byte {
	val := delta["arguments"]
	if val == nil {
		val = delta["partial_arguments"]
	}
	if s, ok := val.(string); ok && !strings.ContainsRune(s, '\ufffd') {
		return []byte(s)
	}
	var focused struct {
		Delta struct {
			Arguments        json.RawMessage `json:"arguments"`
			PartialArguments json.RawMessage `json:"partial_arguments"`
		} `json:"delta"`
	}
	if err := rawArgsBodyDecode(evData, &focused); err == nil {
		tok := bytes.TrimSpace(focused.Delta.Arguments)
		if len(tok) == 0 || bytes.Equal(tok, []byte("null")) {
			tok = bytes.TrimSpace(focused.Delta.PartialArguments)
		}
		if len(tok) > 0 && !bytes.Equal(tok, []byte("null")) {
			if tok[0] == '"' {
				if content, err := protocolhttp.RawStringContent(tok); err == nil {
					return content
				}
			} else {
				return tok
			}
		}
	}
	switch v := val.(type) {
	case string:
		return []byte(v)
	case nil:
		return nil
	default:
		b, _ := json.Marshal(normalizeJSONNumbers(v))
		return b
	}
}
