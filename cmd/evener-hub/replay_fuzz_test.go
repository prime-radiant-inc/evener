package hub

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// replayFuzzSeeds are real assistant/user/tool turns covering each content kind,
// shared by the live-vs-reload fuzz targets. Each is the JSON of one transcript
// Entry; the malformed inputs prove the no-panic floor.
var replayFuzzSeeds = []string{
	// Assistant turn: text + thinking + redacted_thinking + tool_call.
	`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"thinking","thinking":{"text":"reasoning"}},{"kind":"redacted_thinking","thinking":{"redacted":true}},{"kind":"text","text":"answer"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:00Z"}}`,
	// Assistant turn: web_search with provider raw payload + communicate tool_call.
	`{"kind":"entry","seq":2,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"web_search","web_search":{"query":"evener","raw":{"content":[{"type":"web_search_result","url":"https://x","title":"X"}]}}},{"kind":"tool_call","tool_call":{"id":"c2","name":"communicate","arguments":{"message":"hi there"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}`,
	// Assistant turn that answered with a tool call alone: the empty text part
	// the provider returned alongside it must render on neither side.
	`{"kind":"entry","seq":8,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":""},{"kind":"tool_call","tool_call":{"id":"c3","name":"read_file","arguments":{"path":"README.md"}}}]},"timestamp":"2026-06-01T10:00:07Z"}}`,
	// Tool results turn.
	`{"kind":"entry","seq":3,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"output","is_error":false,"tool_state":{"k":"v"}}}]},"timestamp":"2026-06-01T10:00:02Z"}}`,
	// User turn with an inline image + audio + document attachment.
	`{"kind":"entry","seq":4,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"look"},{"kind":"image","image":{"data":"aGVsbG8=","media_type":"image/png"}},{"kind":"audio","audio":{"url":"https://a","media_type":"audio/mp3"}},{"kind":"document","document":{"url":"https://d","media_type":"application/pdf","file_name":"r.pdf"}}]},"timestamp":"2026-06-01T10:00:03Z"}}`,
	// Compaction turn.
	`{"kind":"entry","seq":5,"turn":{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"summary"}]},"timestamp":"2026-06-01T10:00:04Z"}}`,
	// Human-typed steering: carries turn-level provenance (steering_source),
	// which reload must render as the person's own speech rather than as the
	// grey daemon divider (issue #24).
	`{"kind":"entry","seq":6,"turn":{"kind":"STEERING","steering_source":"user","message":{"role":"user","content":[{"kind":"text","text":"new worktree"}]},"timestamp":"2026-06-01T10:00:05Z"}}`,
	// Daemon nudge: same turn kind, deliberately no provenance.
	`{"kind":"entry","seq":7,"turn":{"kind":"STEERING","message":{"role":"user","content":[{"kind":"text","text":"<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>"}]},"timestamp":"2026-06-01T10:00:06Z"}}`,
	// Assistant turn with a rejected tool call: Arguments is the replay-safe {}
	// placeholder, RawArguments preserves the model's original malformed bytes.
	`{"kind":"entry","seq":9,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"running it"},{"kind":"tool_call","tool_call":{"id":"c4","name":"shell","arguments":{},"raw_arguments":"{command: \"ls\", }"}}]},"timestamp":"2026-06-01T10:00:08Z"}}`,
	// Assistant turn with a repairable-malformed tool call: bare keys that
	// RepairJSON can heal. Like the rejected seed, Arguments is {} and
	// RawArguments preserves the original bytes — the live emitter now uses
	// the original bytes too, so live and reload agree.
	`{"kind":"entry","seq":10,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"calling it"},{"kind":"tool_call","tool_call":{"id":"c5","name":"shell","arguments":{},"raw_arguments":"{command: \"ls\"}"}}]},"timestamp":"2026-06-01T10:00:09Z"}}`,
	// Assistant turn with a rejected communicate: Arguments is the replay-safe
	// {} placeholder, RawArguments preserves the model's original malformed
	// bytes. Live emits nothing (CommunicateMessageFromArguments({}) is ""),
	// and reload now defers the raw fallback to the paired result, so the
	// assistant turn alone renders nothing on both sides.
	`{"kind":"entry","seq":11,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c6","name":"communicate","arguments":{},"raw_arguments":"{message: \"hi\"}"}}]},"timestamp":"2026-06-01T10:00:10Z"}}`,
	// Tool-results turn for the rejected communicate: IsError=true surfaces the
	// raw bytes deferred from the assistant turn. Used in the multi-entry
	// metamorphic test (not the single-entry fuzz, which processes one entry).
	`{"kind":"entry","seq":12,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c6","name":"communicate","content":"invalid","is_error":true}}]},"timestamp":"2026-06-01T10:00:11Z"}}`,
	// Assistant turn with a healed communicate: Arguments is {} and
	// RawArguments preserves the malformed original, but the call was repaired
	// and executed successfully. Live delivered the healed message; reload now
	// renders nothing from the raw bytes (the result confirms success).
	`{"kind":"entry","seq":13,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c7","name":"communicate","arguments":{},"raw_arguments":"{message: \"hello\"}"}}]},"timestamp":"2026-06-01T10:00:12Z"}}`,
	// Tool-results turn for the healed communicate: IsError=false, so the raw
	// fallback does not fire — matching live, which delivered the healed message.
	`{"kind":"entry","seq":14,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c7","name":"communicate","content":"{\"accepted\":true}","is_error":false}}]},"timestamp":"2026-06-01T10:00:13Z"}}`,
	`{}`,
	`null`,
	`not json`,
	``,
}

// jsonEqItems reports whether two ThreadItem lists marshal identically.
func jsonEqItems(t *testing.T, a, b any) (bool, []byte, []byte) {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal lhs: %v", err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal rhs: %v", err)
	}
	return bytes.Equal(ab, bb), ab, bb
}

// canonicalEntry returns the entry as it would exist on disk: transcript.Writer
// marshals each entry, so the persisted bytes are the compact form of the
// in-memory turn. Projecting from this canonical form (rather than the raw fuzz
// bytes) keeps the comparison on rendered content, not on the benign whitespace
// normalization that the extra marshal hop applies to json.RawMessage tool
// arguments.
func canonicalEntry(t *testing.T, e transcript.Entry) (transcript.Entry, []byte) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var canon transcript.Entry
	if err := json.Unmarshal(b, &canon); err != nil {
		t.Fatalf("re-decode canonical entry: %v", err)
	}
	return canon, b
}

// FuzzHubReplayLiveVsReload is the full live-vs-reload metamorphic: it compares
// what the user saw LIVE (the appprojector stream) against what the hub renders
// on RELOAD (saved bytes → decodeTranscriptTurn → ProjectTurn), for one
// turn. The live side synthesizes the SessionEvent stream the turn would have
// produced, feeds it through a fresh AppEventProjector, and folds the emitted
// notifications back into final ThreadItems (the streaming projector emits
// item/started + deltas + item/completed; reasoning completion supplies status
// while its text is assembled from deltas — exactly as a client must).
//
// normalizeMetamorphic strips ONLY the documented, legitimate live/reload
// differences before comparing; every strip is cited. Anything outside the
// allow-list is a real reload divergence.
func FuzzHubReplayLiveVsReload(f *testing.F) {
	for _, s := range replayFuzzSeeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		checkLiveVsReload(t, raw)
	})
}

// TestHubReplay_RejectedCallLiveVsReload verifies the live-vs-reload
// metamorphic agrees for a rejected-call tool_call: both sides must surface
// the model's raw argument bytes (SentArguments precedence) and skip the
// Description (intent) for rejected calls. Before the fix, synthesizeLiveEvents
// used Arguments (the {} placeholder) while ProjectTurn used SentArguments
// (the raw bytes), diverging.
func TestHubReplay_RejectedCallLiveVsReload(t *testing.T) {
	const rawArgs = `{command: "ls", }` // malformed JSON — the rejected-call shape
	entryJSON := `{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"running it"},{"kind":"tool_call","tool_call":{"id":"c4","name":"shell","arguments":{},"raw_arguments":` + `"` + strings.ReplaceAll(rawArgs, `"`, `\"`) + `"` + `}}]},"timestamp":"2026-06-01T10:00:00Z"}}`
	checkLiveVsReload(t, []byte(entryJSON))
}

// checkLiveVsReloadMultiEntry runs the live-vs-reload metamorphic across TWO
// entries (an assistant turn followed by its paired tool-results turn), sharing
// one toolNames map on the reload side the way the hub's full read does. This is
// where the communicate raw fallback's result-gating is exercised: the assistant
// turn defers the raw bytes, and the result turn's IsError determines whether
// they surface. The single-entry checkLiveVsReload cannot test this because it
// projects one turn in isolation.
//
// commRawArgs threads the assistant turn's raw communicate bytes into the live
// side: a rejected communicate surfaces them as a CommunicateData (modeled live),
// matching the reload side's result-gated raw fallback. A healed communicate
// (IsError=false) renders nothing on both sides.
func checkLiveVsReloadMultiEntry(t *testing.T, assistantJSON, resultJSON []byte, commRawArgs string) {
	t.Helper()
	var ae, re transcript.Entry
	if json.Unmarshal(assistantJSON, &ae) != nil {
		t.Fatalf("decode assistant entry: %s", assistantJSON)
	}
	if json.Unmarshal(resultJSON, &re) != nil {
		t.Fatalf("decode result entry: %s", resultJSON)
	}
	acanon, acanonBytes := canonicalEntry(t, ae)
	rcanon, rcanonBytes := canonicalEntry(t, re)

	// Live side: synthesize events for both turns and drive one projector. The
	// assistant turn emits nothing for a communicate with Arguments={} (both
	// rejected and healed share that shape). The result turn's IsError
	// determines whether the raw bytes surface: rejected emits a CommunicateData
	// with the raw bytes (modeled live matching reload's result-gated fallback);
	// healed emits nothing.
	var liveEvents []events.SessionEvent
	liveEvents = append(liveEvents, mustSynthesize(t, acanon.Turn)...)
	for _, p := range rcanon.Turn.Message.Content {
		if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
			continue
		}
		if p.ToolResult.Name != "communicate" {
			continue
		}
		if p.ToolResult.IsError && commRawArgs != "" {
			liveEvents = append(liveEvents, events.New(events.CommunicateData{Message: commRawArgs}))
		}
		// A healed communicate (IsError=false) delivered its message live, not
		// the raw bytes; the synthesizer models that by emitting nothing.
	}
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}
	live := normalizeMetamorphic(foldLiveItems(notes))

	// Reload side: project both turns with a shared toolNames map, the way the
	// hub's full-transcript read threads the map across entries.
	toolNames := map[string]string{}
	aReconstructed, ok := decodeTranscriptTurn(acanonBytes)
	if !ok {
		t.Fatalf("hub decode rejected assistant entry: %s", acanonBytes)
	}
	rReconstructed, ok := decodeTranscriptTurn(rcanonBytes)
	if !ok {
		t.Fatalf("hub decode rejected result entry: %s", rcanonBytes)
	}
	reload := normalizeMetamorphic(append(
		apptranscript.ProjectTurn("turn_1", 1, aReconstructed, toolNames, nil, apptranscript.ToolResultOutputImages),
		apptranscript.ProjectTurn("turn_2", 2, rReconstructed, toolNames, nil, apptranscript.ToolResultOutputImages)...,
	))

	if eq, a, b := jsonEqItems(t, live, reload); !eq {
		t.Fatalf("live-vs-reload multi-entry metamorphic diverged:\n live  =%s\n reload=%s\n assistant=%s\n result=%s", a, b, acanonBytes, rcanonBytes)
	}
}

// mustSynthesize returns the live event stream for a turn, failing the test if
// the turn kind is unsupported (the multi-entry test exercises kinds that must
// have a live path).
func mustSynthesize(t *testing.T, turn schema.Turn) []events.SessionEvent {
	t.Helper()
	evs, supported := synthesizeLiveEvents(turn)
	if !supported {
		t.Fatalf("synthesizeLiveEvents returned unsupported for turn kind %s", turn.Kind)
	}
	return evs
}

// TestHubReplay_RejectedCommunicateLiveVsReload verifies that a rejected
// communicate (Arguments={}, RawArguments=malformed, IsError result) renders the
// raw bytes on BOTH live and reload. The assistant turn defers the raw bytes;
// the result turn's IsError surfaces them. Live models the same rule so both
// sides agree — the contract FuzzHubReplayLiveVsReload enforces.
func TestHubReplay_RejectedCommunicateLiveVsReload(t *testing.T) {
	const rawArgs = `{message: "hi"}` // malformed JSON — bare key
	escaped := strings.ReplaceAll(rawArgs, `"`, `\"`)
	assistantJSON := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c6","name":"communicate","arguments":{},"raw_arguments":"` + escaped + `"}}]},"timestamp":"2026-06-01T10:00:00Z"}}`)
	resultJSON := []byte(`{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c6","name":"communicate","content":"invalid","is_error":true}}]},"timestamp":"2026-06-01T10:00:01Z"}}`)
	checkLiveVsReloadMultiEntry(t, assistantJSON, resultJSON, rawArgs)
}

// TestHubReplay_RepairedCommunicateLiveVsReload verifies that a healed
// communicate (Arguments={}, RawArguments=malformed, IsError=false result)
// renders NOTHING on both live and reload. The assistant turn defers the raw
// bytes; the result turn's success confirms the call was healed, so no raw
// fallback fires. Live delivered the healed message (not the raw bytes), and
// reload now matches by rendering nothing from the raw bytes.
func TestHubReplay_RepairedCommunicateLiveVsReload(t *testing.T) {
	const rawArgs = `{message: "hello"}` // malformed JSON — bare key, healed
	escaped := strings.ReplaceAll(rawArgs, `"`, `\"`)
	assistantJSON := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c7","name":"communicate","arguments":{},"raw_arguments":"` + escaped + `"}}]},"timestamp":"2026-06-01T10:00:00Z"}}`)
	resultJSON := []byte(`{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c7","name":"communicate","content":"{\"accepted\":true}","is_error":false}}]},"timestamp":"2026-06-01T10:00:01Z"}}`)
	checkLiveVsReloadMultiEntry(t, assistantJSON, resultJSON, rawArgs)
}

// checkLiveVsReload runs the live-vs-reload metamorphic on one entry's JSON: a
// content kind that survives one projection path but not the other (or shifts
// position) makes the two item lists diverge. Shared by the raw-byte target
// above and the structure-aware target below.
func checkLiveVsReload(t *testing.T, raw []byte) {
	t.Helper()
	var e transcript.Entry
	if json.Unmarshal(raw, &e) != nil {
		return
	}
	canon, canonBytes := canonicalEntry(t, e)

	liveEvents, supported := synthesizeLiveEvents(canon.Turn)
	if !supported {
		return // turn kind has no clean item-producing live path (see synthesizer)
	}

	// Live side: drive a fresh projector and fold its notifications.
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}
	live := normalizeMetamorphic(foldLiveItems(notes))

	// Reload side: the hub's own path off the saved bytes.
	reconstructed, ok := decodeTranscriptTurn(canonBytes)
	if !ok {
		t.Fatalf("hub decode rejected the canonical entry: %s", canonBytes)
	}
	reload := normalizeMetamorphic(apptranscript.ProjectTurn("turn_1", 1, reconstructed, map[string]string{}, nil, apptranscript.ToolResultOutputImages))

	if eq, a, b := jsonEqItems(t, live, reload); !eq {
		t.Fatalf("live-vs-reload metamorphic diverged:\n live  =%s\n reload=%s\n entry=%s", a, b, canonBytes)
	}
}

// synthesizeLiveEvents builds the SessionEvent stream the live path would have
// emitted for turn, covering the content kinds that have a faithful live event
// representation. It returns supported=false for turn kinds whose live rendering
// is not a per-turn item stream (steering is a distinct notification, not an
// item; system turns produce nothing), so the metamorphic skips them. Content
// kinds with NO live event (web_search, redacted_thinking, and audio/document
// user attachments) are intentionally not synthesized here and are dropped from
// the reload side by normalizeMetamorphic's allow-list.
func synthesizeLiveEvents(turn schema.Turn) ([]events.SessionEvent, bool) {
	var out []events.SessionEvent
	add := func(d events.EventData) { out = append(out, events.New(d)) }

	switch turn.Kind {
	case schema.TurnUserInput:
		var imgs []events.UserInputImage
		for _, p := range turn.Message.Content {
			// Mirror ImagesFromContent: only inline-byte images render; a
			// URL-only image has no bytes and is skipped on both sides.
			if p.Kind == llm.ContentImage && p.Image != nil && len(p.Image.Data) > 0 {
				imgs = append(imgs, events.UserInputImage{MediaType: p.Image.MediaType, Data: p.Image.Data})
			}
		}
		add(events.UserInputData{Text: turn.Message.Text(), Images: imgs})
		return out, true

	case schema.TurnAssistant:
		for _, p := range turn.Message.Content {
			switch p.Kind {
			case llm.ContentText:
				// Emitted even when the text is empty, because that is what the
				// live path really does: a round answering with tool calls alone
				// still runs the text lifecycle, since ASSISTANT_TEXT_END carries
				// the round's usage. Reload renders nothing for such a part, so
				// this is exactly where an empty live agent message would diverge.
				add(events.AssistantTextStartData{})
				add(events.AssistantTextEndData{Text: p.Text})
			case llm.ContentThinking:
				if p.Thinking != nil && p.Thinking.Text != "" {
					add(events.ReasoningSummaryDeltaData{Delta: p.Thinking.Text})
				}
			case llm.ContentToolCall:
				if p.ToolCall == nil {
					continue
				}
				// communicate surfaces live as EventCommunicate, not a tool item
				// (the live ToolCallStart for communicate is suppressed). Reload
				// maps a well-formed communicate tool_call to the same
				// agentMessage. A communicate with Arguments={} and
				// RawArguments set is ambiguous on the assistant turn alone
				// (rejected vs healed-and-executed share that durable shape),
				// so both live and reload render nothing here: live emits no
				// EventCommunicate (CommunicateMessageFromArguments({}) is
				// ""), and reload defers the raw fallback to the paired result
				// turn. The single-entry metamorphic compares the assistant
				// turn alone, so both sides agree (nothing). The multi-entry
				// test below exercises the paired result turn, which carries
				// the error/PrevalOnly status that disambiguates.
				if p.ToolCall.Name == "communicate" {
					if msg := apptranscript.CommunicateMessageFromArguments(p.ToolCall.Arguments); msg != "" {
						add(events.CommunicateData{Message: msg})
					}
					continue
				}
				// Rejected call: show raw bytes, skip intent (mirrors ProjectTurn).
				argumentsJSON := p.ToolCall.SentArguments()
				description := ""
				if p.ToolCall.RawArguments == "" {
					description = apptranscript.ToolIntentFromArguments(p.ToolCall.Arguments)
				}
				add(events.ToolCallStartData{
					ToolName:      p.ToolCall.Name,
					CallID:        p.ToolCall.ID,
					ArgumentsJSON: argumentsJSON,
					Description:   description,
				})
			}
		}
		return out, true

	case schema.TurnTool, schema.TurnToolResults:
		// This entry IS a round: the daemon writes one of these once every call
		// in the round has ended, and announces right afterwards which of those
		// calls a reader can now fetch images for (kata v3dv). Both halves are
		// synthesized here, in the same order, or the live side never catches up
		// to the reload side it is being compared against.
		var readableImageCallIDs []string
		for _, p := range turn.Message.Content {
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			// communicate results are suppressed live (its start was suppressed).
			// On reload, a communicate result is skipped here too â the raw
			// fallback (if any) was deferred from the assistant turn and is
			// surfaced by ProjectTurn's result-gated branch, not by this
			// synthesizer. The single-entry metamorphic sees a lone result turn
			// with an empty toolNames map, so neither side emits anything. The
			// multi-entry test (checkLiveVsReloadMultiEntry) threads the raw
			// bytes into the live side to exercise the result-gated fallback.
			if p.ToolResult.Name == "communicate" {
				continue
			}
			end := events.ToolCallEndData{
				ToolName:  p.ToolResult.Name,
				CallID:    p.ToolResult.ToolCallID,
				ToolState: p.ToolResult.ToolState,
			}
			// Mirror agent/session_tools.go: a result carrying image bytes
			// describes them on the event, by the same rule the reload side
			// projects them (kata 2fxm). Synthesizing this by hand instead
			// would let the two descriptions drift apart unnoticed.
			if img, ok := events.ToolResultOutputImage(p.ToolResult.Name, p.ToolResult.ImageData, p.ToolResult.ImageMediaType); ok {
				end.OutputImages = []events.OutputImage{img}
				readableImageCallIDs = append(readableImageCallIDs, p.ToolResult.ToolCallID)
			}
			content := apptranscript.StringifyToolContent(p.ToolResult.Content)
			if p.ToolResult.IsError {
				end.Error = content
			} else {
				end.Output = content
			}
			add(end)
		}
		if len(readableImageCallIDs) > 0 {
			add(events.ToolResultImagesPersistedData{CallIDs: readableImageCallIDs})
		}
		return out, true

	case schema.TurnCheckpoint, schema.TurnSummary:
		add(events.CompactionTurnData{Kind: string(turn.Kind), Text: turn.Message.Text()})
		return out, true

	default:
		return nil, false
	}
}

// foldLiveItems reduces the projector's notification stream into the final
// ordered ThreadItems a client would render: item/started seeds an item,
// item/completed settles it, and reasoning/agentMessage deltas accumulate into
// the item's text. Reasoning completion carries only terminal status, so its
// accumulated text survives that frame. turn/completed carrying embedded
// items (the no-active-turn systemAnnouncement path) contributes those items.
func foldLiveItems(notes []appprojector.AppNotification) []appwire.ThreadItem {
	items := map[string]*appwire.ThreadItem{}
	var order []string
	get := func(id string) *appwire.ThreadItem {
		it := items[id]
		if it == nil {
			it = &appwire.ThreadItem{}
			items[id] = it
			order = append(order, id)
		}
		return it
	}
	put := func(it appwire.ThreadItem) { *get(it.ID) = it }

	for _, n := range notes {
		switch n.Method {
		case appwire.NotifyItemStarted, appwire.NotifyItemCompleted:
			// appwire_projection.go's own item/started|completed sites now send
			// appwire.ItemLifecycleParams (kcb5), not map[string]any.
			if p, ok := n.Params.(appwire.ItemLifecycleParams); ok {
				item := p.Item
				if n.Method == appwire.NotifyItemCompleted && item.Type == "reasoning" && item.Text == "" {
					item.Text = get(item.ID).Text
				}
				put(item)
			}
		case appwire.NotifyReasoningSummaryDelta:
			if p, ok := n.Params.(appwire.ReasoningSummaryDeltaParams); ok {
				get(p.ItemID).Text += p.Delta
			}
		case appwire.NotifyAgentMessageDelta:
			if p, ok := n.Params.(appwire.AgentMessageDeltaParams); ok {
				get(p.ItemID).Text += p.Delta
			}
		case appwire.NotifyTurnCompleted:
			if m, ok := n.Params.(map[string]any); ok {
				if turn, ok := m["turn"].(appwire.Turn); ok {
					for _, it := range turn.Items {
						put(it)
					}
				}
			}
		}
	}

	out := make([]appwire.ThreadItem, 0, len(order))
	for _, id := range order {
		out = append(out, *items[id])
	}
	return out
}

// normalizeMetamorphic strips the legitimate live/reload differences before
// comparison. Each strip is load-bearing and justified; an over-broad entry
// would mask a real reload carry-through bug.
func normalizeMetamorphic(items []appwire.ThreadItem) []appwire.ThreadItem {
	out := make([]appwire.ThreadItem, 0, len(items))
	for _, it := range items {
		// ALLOW-LIST (reload-only renderings with no live event path):
		//   - web_search: the live appprojector has no web_search event; the hub
		//     renders web_search ONLY on reload (added in ec96619c). 4a covers its
		//     carry-through fidelity.
		if it.Type == "commandExecution" && it.ToolName == "web_search" {
			continue
		}
		//   - redacted thinking: there is no live reasoning-summary delta for
		//     redacted thinking, so nothing renders live; reload emits a
		//     "[redacted thinking]" placeholder. 4a covers its carry-through.
		if it.Type == "reasoning" && it.Text == "[redacted thinking]" {
			continue
		}

		// Synthetic / stream-derived identity and per-turn status: item IDs are
		// index-derived on reload and counter-derived live; CallID is
		// stream-derived; Status legitimately differs (live in-progress vs reload
		// completed for the same item). None of these are rendered content.
		it.ID = ""
		it.TurnID = ""
		it.CallID = ""
		it.Status = ""
		it.StartedAt = nil
		it.CompletedAt = nil
		it.TranscriptEntryIndex = 0

		it.Images = normalizeMetamorphicImages(it.Images)
		out = append(out, it)
	}
	return out
}

// normalizeMetamorphicImages reduces input attachments to their media type:
// image enrichment (live "image" type + inline Data + Name vs reload's
// "input_image" + empty Name) collapses to the shared media type. Nothing is
// dropped - both sides carry pictures and only pictures, so any extra entry
// on either side is a real divergence for the differential to report.
func normalizeMetamorphicImages(images []appwire.InputItem) []appwire.InputItem {
	var out []appwire.InputItem
	for _, img := range images {
		out = append(out, appwire.InputItem{MediaType: img.MediaType})
	}
	return out
}

// FuzzHubReplayLiveVsReloadStructured drives the live-vs-reload differential with
// ALWAYS-VALID entries built across the content kinds, so the search explores
// kind COMBINATIONS instead of stalling on the broken JSON that raw-byte mutation
// mostly produces. buildReplayEntry matches content kinds to the turn kind that
// can carry them and routes the fuzzer into the renderable text fields.
func FuzzHubReplayLiveVsReloadStructured(f *testing.F) {
	f.Add(byte(0), byte(0xff), "answer", "reasoning", "evener query", "shell", "ls -la")
	// The same assistant turn with nothing to say: tool calls only.
	f.Add(byte(0), byte(0xff), "", "reasoning", "evener query", "shell", "ls -la")
	f.Add(byte(1), byte(0xff), "look", "", "", "", "")
	f.Add(byte(2), byte(1), "tool output", "", "", "", "")
	f.Add(byte(2), byte(7), "tool output with images", "", "", "", "")
	f.Add(byte(3), byte(1), "summary text", "", "", "", "")

	f.Fuzz(func(t *testing.T, turnSel, partsSel byte, text, think, query, name, cmd string) {
		checkLiveVsReload(t, buildReplayEntry(turnSel, partsSel, text, think, query, name, cmd))
	})
}

// buildReplayEntry assembles a valid transcript-entry JSON for one turn kind,
// selecting a subset of that kind's content parts via partsSel and filling the
// renderable fields from the fuzzer's strings (JSON-escaped).
func buildReplayEntry(turnSel, partsSel byte, text, think, query, name, cmd string) []byte {
	js := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	turnKinds := []string{"ASSISTANT", "USER_INPUT", "TOOL_RESULTS", "SUMMARY"}
	turnKind := turnKinds[int(turnSel)%len(turnKinds)]

	var role string
	var menu []string
	switch turnKind {
	case "ASSISTANT":
		role = "assistant"
		menu = []string{
			`{"kind":"text","text":` + js(text) + `}`,
			`{"kind":"thinking","thinking":{"text":` + js(think) + `}}`,
			`{"kind":"redacted_thinking","thinking":{"redacted":true}}`,
			`{"kind":"web_search","web_search":{"query":` + js(query) + `,"raw":{"content":[{"type":"web_search_result","url":"https://x","title":` + js(query) + `}]}}}`,
			`{"kind":"tool_call","tool_call":{"id":"c1","name":` + js(name) + `,"arguments":{"command":` + js(cmd) + `}}}`,
			`{"kind":"tool_call","tool_call":{"id":"c2","name":"communicate","arguments":{"message":` + js(text) + `}}}`,
		}
	case "USER_INPUT":
		role = "user"
		menu = []string{
			`{"kind":"text","text":` + js(text) + `}`,
			`{"kind":"image","image":{"data":"aGVsbG8=","media_type":"image/png"}}`,
			`{"kind":"audio","audio":{"url":"https://a","media_type":"audio/mp3"}}`,
			`{"kind":"document","document":{"url":"https://d","media_type":"application/pdf","file_name":"r.pdf"}}`,
		}
	case "TOOL_RESULTS":
		role = "tool"
		menu = []string{
			`{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":` + js(text) + `,"is_error":false}}`,
			`{"kind":"tool_result","tool_result":{"tool_call_id":"c2","name":"screenshot","content":` + js(text) + `,"is_error":false,"image_data":"aGVsbG8=","image_media_type":"image/png"}}`,
			`{"kind":"tool_result","tool_result":{"tool_call_id":"c3","name":"read_file","content":` + js(text) + `,"is_error":false,"image_data":"aGVsbG8="}}`,
		}
	default: // SUMMARY
		role = "assistant"
		menu = []string{`{"kind":"text","text":` + js(text) + `}`}
	}

	var parts []string
	for i := range menu {
		if partsSel&(1<<uint(i)) != 0 {
			parts = append(parts, menu[i])
		}
	}
	if len(parts) == 0 {
		parts = append(parts, menu[0]) // content must be non-empty
	}
	return []byte(`{"kind":"entry","seq":1,"turn":{"kind":"` + turnKind +
		`","message":{"role":"` + role + `","content":[` + strings.Join(parts, ",") +
		`]},"timestamp":"2026-06-01T10:00:00Z"}}`)
}
