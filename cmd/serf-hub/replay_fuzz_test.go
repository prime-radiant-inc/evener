package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appprojector"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
)

// replayFuzzSeeds are real assistant/user/tool turns covering each content kind,
// shared by both Target-4 fuzz functions. Each is the JSON of one transcript
// Entry. The malformed inputs are inline bootstrap; richer per-kind turns arrive
// from 8.4.
var replayFuzzSeeds = []string{
	// Assistant turn: text + thinking + redacted_thinking + tool_call.
	`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"thinking","thinking":{"text":"reasoning"}},{"kind":"redacted_thinking","thinking":{"redacted":true}},{"kind":"text","text":"answer"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:00Z"}}`,
	// Assistant turn: web_search with provider raw payload + communicate tool_call.
	`{"kind":"entry","seq":2,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"web_search","web_search":{"query":"serf","raw":{"content":[{"type":"web_search_result","url":"https://x","title":"X"}]}}},{"kind":"tool_call","tool_call":{"id":"c2","name":"communicate","arguments":{"message":"hi there"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}`,
	// Tool results turn.
	`{"kind":"entry","seq":3,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"output","is_error":false,"tool_state":{"k":"v"}}}]},"timestamp":"2026-06-01T10:00:02Z"}}`,
	// User turn with an inline image + audio + document attachment.
	`{"kind":"entry","seq":4,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"look"},{"kind":"image","image":{"data":"aGVsbG8=","media_type":"image/png"}},{"kind":"audio","audio":{"url":"https://a","media_type":"audio/mp3"}},{"kind":"document","document":{"url":"https://d","media_type":"application/pdf","file_name":"r.pdf"}}]},"timestamp":"2026-06-01T10:00:03Z"}}`,
	// Compaction turn.
	`{"kind":"entry","seq":5,"turn":{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"summary"}]},"timestamp":"2026-06-01T10:00:04Z"}}`,
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
// bytes) keeps 4a focused on ReplayPart carry-through structure, not on the
// benign whitespace normalization that the extra marshal hop applies to
// json.RawMessage tool arguments.
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

// FuzzHubReplayCarryThrough isolates the hub reload round-trip
// (Entry → ReplayEntry → replayTurnToAgentTurn) against the shared projector.
// It projects both the canonical on-disk turn and its reload-roundtripped
// reconstruction through the SAME apptranscript.ProjectTurn and asserts the two
// item lists are equal. Any divergence is attributable to the round-trip alone:
// if ReplayPart / replayTurnToAgentTurn fails to carry a content kind (thinking,
// web_search, audio, document, image, tool_call, tool_result), the reload
// projection loses items the original kept. This generalizes the hand-fixed
// regressions 0a6b65b0 (thinking) and ec96619c (web_search/audio/document).
func FuzzHubReplayCarryThrough(f *testing.F) {
	for _, s := range replayFuzzSeeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var e transcript.Entry
		if json.Unmarshal(raw, &e) != nil {
			return // rejected input: no-panic floor proven
		}
		canon, canonBytes := canonicalEntry(t, e)

		var re hubcore.ReplayEntry
		if err := json.Unmarshal(canonBytes, &re); err != nil {
			t.Fatalf("decode ReplayEntry from canonical bytes: %v", err)
		}
		reconstructed, _ := replayTurnToAgentTurn(re.Turn)

		live := stripSyntheticIDs(projectIsolated(canon.Turn))
		reload := stripSyntheticIDs(projectIsolated(reconstructed))
		if eq, a, b := jsonEqItems(t, live, reload); !eq {
			t.Fatalf("hub reload carry-through diverged:\n original=%s\n reload  =%s\n entry=%s", a, b, canonBytes)
		}
	})
}

// projectIsolated projects a turn through ProjectTurn with a fresh toolNames map
// (ProjectTurn mutates and deletes from it) and the default image projector.
func projectIsolated(turn schema.Turn) []appwire.ThreadItem {
	return apptranscript.ProjectTurn("turn_1", 1, turn, map[string]string{}, nil)
}

// stripSyntheticIDs zeroes the index-derived ID and the constant TurnID so the
// comparison is over rendered CONTENT, not synthetic numbering. The reload path
// legitimately drops content parts whose kind it does not recognize (an honest
// transform), which compacts the content slice and shifts the array index that
// ProjectTurn bakes into item IDs. That index drift is invisible in production
// (the live appprojector uses an unrelated global-counter ID scheme, so reload
// IDs never need to match live IDs) and is not the carry-through bug class. A
// genuinely DROPPED renderable item still shows up as a list length/content
// difference, which this normalization preserves.
func stripSyntheticIDs(items []appwire.ThreadItem) []appwire.ThreadItem {
	out := append([]appwire.ThreadItem(nil), items...)
	for i := range out {
		out[i].ID = ""
		out[i].TurnID = ""
	}
	return out
}

// FuzzHubReplayLiveVsReload is the full live-vs-reload metamorphic: it compares
// what the user saw LIVE (the appprojector stream) against what the hub renders
// on RELOAD (Entry → ReplayEntry → replayTurnToAgentTurn → ProjectTurn), for one
// turn. The live side synthesizes the SessionEvent stream the turn would have
// produced, feeds it through a fresh AppEventProjector, and folds the emitted
// notifications back into final ThreadItems (the streaming projector emits
// item/started + deltas + item/completed; reasoning never emits a completed
// item, so its text is assembled from deltas — exactly as a client must).
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

	// Reload side: the same path as 4a.
	var re hubcore.ReplayEntry
	if err := json.Unmarshal(canonBytes, &re); err != nil {
		t.Fatalf("decode ReplayEntry: %v", err)
	}
	reconstructed, _ := replayTurnToAgentTurn(re.Turn)
	reload := normalizeMetamorphic(apptranscript.ProjectTurn("turn_1", 1, reconstructed, map[string]string{}, nil))

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
				if p.Text != "" {
					add(events.AssistantTextStartData{})
					add(events.AssistantTextEndData{Text: p.Text})
				}
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
				// maps the communicate tool_call to the same agentMessage.
				if p.ToolCall.Name == "communicate" {
					if msg := apptranscript.CommunicateMessageFromArguments(p.ToolCall.Arguments); msg != "" {
						add(events.CommunicateData{Message: msg})
					}
					continue
				}
				add(events.ToolCallStartData{
					ToolName:      p.ToolCall.Name,
					CallID:        p.ToolCall.ID,
					ArgumentsJSON: string(p.ToolCall.Arguments),
					Description:   apptranscript.ToolIntentFromArguments(p.ToolCall.Arguments),
				})
			}
		}
		return out, true

	case schema.TurnTool, schema.TurnToolResults:
		for _, p := range turn.Message.Content {
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			// communicate results are suppressed live (its start was suppressed)
			// and skipped on reload; omit to match both.
			if p.ToolResult.Name == "communicate" {
				continue
			}
			end := events.ToolCallEndData{
				ToolName:  p.ToolResult.Name,
				CallID:    p.ToolResult.ToolCallID,
				ToolState: p.ToolResult.ToolState,
			}
			content := apptranscript.StringifyToolContent(p.ToolResult.Content)
			if p.ToolResult.IsError {
				end.Error = content
			} else {
				end.Output = content
			}
			add(end)
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
// item/completed replaces it, and reasoning/agentMessage deltas accumulate into
// the item's text (reasoning items never receive an item/completed, so the delta
// fold is the only way their text materializes). turn/completed carrying embedded
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
			if m, ok := n.Params.(map[string]any); ok {
				if it, ok := m["item"].(appwire.ThreadItem); ok {
					put(it)
				}
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

// normalizeMetamorphicImages reduces input attachments to their media type and
// drops the audio/document attachments that exist only on reload: the live
// EventUserInput payload (UserInputData) carries images only, with no field for
// audio/document attachments, so those render on reload alone. Image enrichment
// (live "image" type + inline Data + Name vs reload's "input_image" + empty
// Name) collapses to the shared media type.
func normalizeMetamorphicImages(images []appwire.InputItem) []appwire.InputItem {
	var out []appwire.InputItem
	for _, img := range images {
		if img.Type == "input_audio" || img.Type == "input_document" {
			continue
		}
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
	f.Add(byte(0), byte(0xff), "answer", "reasoning", "serf query", "shell", "ls -la")
	f.Add(byte(1), byte(0xff), "look", "", "", "", "")
	f.Add(byte(2), byte(1), "tool output", "", "", "", "")
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
