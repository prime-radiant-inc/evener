package hub

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/argrepair"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	// Assistant turn with a communicate whose Arguments is the replay-safe {}
	// placeholder and RawArguments preserves the model's original malformed
	// bytes. In the one-entry oracle this is unpaired at transcript close, so
	// both sides recover the repairable message. The paired multi-entry tests
	// below disambiguate rejected from healed calls using the result.
	`{"kind":"entry","seq":11,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c6","name":"communicate","arguments":{},"raw_arguments":"{message: \"hi\"}"}}]},"timestamp":"2026-06-01T10:00:10Z"}}`,
	// Tool-results turn for the rejected communicate: IsError=true surfaces the
	// raw bytes deferred from the assistant turn. Used in the multi-entry
	// metamorphic test (not the single-entry fuzz, which processes one entry).
	`{"kind":"entry","seq":12,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c6","name":"communicate","content":"invalid","is_error":true}}]},"timestamp":"2026-06-01T10:00:11Z"}}`,
	// Assistant turn with a healed communicate: Arguments is {} and RawArguments
	// preserves the malformed original. The one-entry oracle treats it as
	// unpaired at close; the paired multi-entry test confirms the successful
	// result consumes the pending call and renders the same healed message.
	`{"kind":"entry","seq":13,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c7","name":"communicate","arguments":{},"raw_arguments":"{message: \"hello\"}"}}]},"timestamp":"2026-06-01T10:00:12Z"}}`,
	// Tool-results turn for the healed communicate: IsError=false, so the raw
	// fallback does not fire — matching live, which delivered the healed message.
	`{"kind":"entry","seq":14,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c7","name":"communicate","content":"{\"accepted\":true}","is_error":false}}]},"timestamp":"2026-06-01T10:00:13Z"}}`,
	// Assistant turn whose communicate has no paired result at transcript close.
	// The fixed call ID and message make this the deterministic flush-path seed.
	`{"kind":"entry","seq":15,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"call_unpaired_oracle","name":"communicate","arguments":{"message":"unpaired at close"}}}]},"timestamp":"2026-06-01T10:00:14Z"}}`,
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

// knownClosingCallIDEchoes identifies one deliberately excluded product
// divergence. A CallID-scoped live communicate commits its preview even when its
// message repeats assistant text, while close-time reload suppresses that echo.
// Tracked production follow-up: "hub: CallID communicate echo renders twice live
// but once after reload" (#2653). Remove this exclusion when it is resolved.
//
// Keep synthesizeLiveEvents faithful; the caller removes only the extra live item
// with the matching CallID and still compares every other item. Derive the echo
// state through ProjectTurn itself so the exclusion follows the reload registry's
// rule: only a non-empty final text candidate updates the state, while an
// assistant record whose final candidate is empty preserves prior registry state.
func knownClosingCallIDEchoes(turn schema.Turn) map[string]string {
	if turn.Kind != schema.TurnAssistant {
		return nil
	}
	const turnID = "replay_oracle_echo_detector"
	reg := apptranscript.NewToolCallRegistry()
	apptranscript.ProjectTurn(turnID, 0, turn, reg, nil, apptranscript.ToolResultOutputImages)
	if reg.LastAssistantTurnID != turnID || reg.LastAssistantText == "" {
		return nil
	}
	echoes := make(map[string]string)
	for _, part := range turn.Message.Content {
		if part.Kind != llm.ContentToolCall || part.ToolCall == nil || part.ToolCall.Name != "communicate" || part.ToolCall.ID == "" {
			continue
		}
		repaired := argrepair.RepairJSON([]byte(part.ToolCall.SentArguments()))
		normalized := apptranscript.NormalizeCommunicateArguments(repaired)
		message := apptranscript.CommunicateMessageFromArguments(normalized)
		if message != "" && apptranscript.EchoesAssistantText(reg.LastAssistantText, message) {
			echoes[part.ToolCall.ID] = message
		}
	}
	return echoes
}

func excludeKnownClosingCallIDEchoes(turn schema.Turn, items []appwire.ThreadItem) []appwire.ThreadItem {
	echoes := knownClosingCallIDEchoes(turn)
	if len(echoes) == 0 {
		return items
	}
	out := make([]appwire.ThreadItem, 0, len(items))
	for _, item := range items {
		message, known := echoes[item.CallID]
		if known && item.Type == "agentMessage" && strings.TrimSpace(item.Text) == strings.TrimSpace(message) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func TestClosingCallIDEchoDivergenceExclusionIsNarrow(t *testing.T) {
	tests := []struct {
		name    string
		texts   []string
		callID  string
		message string
		want    bool
	}{
		{name: "last individual text echoes", texts: []string{"first", "last"}, callID: "call-1", message: "last", want: true},
		{name: "earlier text does not echo", texts: []string{"first", "last"}, callID: "call-1", message: "first"},
		{name: "later whitespace leaves fresh reload echo state empty", texts: []string{"first", "  "}, callID: "call-1", message: "first"},
		{name: "missing call ID uses projector dedup", texts: []string{"same"}, message: "same"},
		{name: "different message remains covered", texts: []string{"shown"}, callID: "call-1", message: "delivered"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts := make([]llm.ContentPart, 0, len(tc.texts)+1)
			for _, text := range tc.texts {
				parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: text})
			}
			arguments, err := json.Marshal(map[string]string{"message": tc.message})
			if err != nil {
				t.Fatalf("marshal communicate arguments: %v", err)
			}
			parts = append(parts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID: tc.callID, Name: "communicate", Arguments: arguments,
			}})
			turn := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: parts}}
			if got := len(knownClosingCallIDEchoes(turn)) > 0; got != tc.want {
				t.Fatalf("knownClosingCallIDEchoes() matched = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("whitespace-only assistant record keeps prior reload echo state", func(t *testing.T) {
		reg := apptranscript.NewToolCallRegistry()
		apptranscript.ProjectTurn("turn_1", 1, schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "shown"},
			}},
		}, reg, nil, apptranscript.ToolResultOutputImages)
		apptranscript.ProjectTurn("turn_2", 2, schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "  "},
			}},
		}, reg, nil, apptranscript.ToolResultOutputImages)
		if reg.LastAssistantText != "shown" || reg.LastAssistantTurnID != "turn_1" {
			t.Fatalf("whitespace-only record changed prior echo state: text=%q turn=%q", reg.LastAssistantText, reg.LastAssistantTurnID)
		}
		if !apptranscript.EchoesAssistantText(reg.LastAssistantText, "shown") {
			t.Fatal("preserved reload state did not suppress the repeated assistant message")
		}
	})

	t.Run("mixed echo and non-echo items preserve non-echo", func(t *testing.T) {
		arguments := func(message string) json.RawMessage {
			b, err := json.Marshal(map[string]string{"message": message})
			if err != nil {
				t.Fatalf("marshal communicate arguments: %v", err)
			}
			return b
		}
		turn := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "shown"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "echo-call", Name: "communicate", Arguments: arguments("shown")}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "other-call", Name: "communicate", Arguments: arguments("different")}},
		}}}
		items := []appwire.ThreadItem{
			{Type: "agentMessage", CallID: "echo-call", Text: "shown"},
			{Type: "agentMessage", CallID: "other-call", Text: "different"},
		}
		got := excludeKnownClosingCallIDEchoes(turn, items)
		if len(got) != 1 || got[0].CallID != "other-call" || got[0].Text != "different" {
			t.Fatalf("exclusion removed more than the known echo item: %+v", got)
		}
	})
}

// FuzzHubReplayLiveVsReload is the full live-vs-reload metamorphic: it compares
// what the user saw LIVE (the appprojector stream) against what the hub renders
// on RELOAD (saved transcript → computePastEntryTurns), for one turn. The live
// side synthesizes the SessionEvent stream the turn would have
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
// side: a rejected communicate surfaces them as a settled failed
// commandExecution (modeled live), matching the reload side's result-gated raw
// fallback. A healed communicate (IsError=false) renders the delivered message
// on both sides.
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
		// A rejected communicate (IsError=true, PrevalOnly=true) was never
		// executed — the START was suppressed, but the END now emits a
		// settled failed commandExecution item (matching what reload renders
		// from the deferred CommRawArgs). Model the same ToolCallEndData the
		// live session would emit for the rejected call.
		if p.ToolResult.IsError {
			liveEvents = append(liveEvents, events.New(events.ToolCallEndData{
				ToolName:      "communicate",
				CallID:        p.ToolResult.ToolCallID,
				ArgumentsJSON: commRawArgs,
				Error:         apptranscript.StringifyToolContent(p.ToolResult.Content),
				PrevalOnly:    p.ToolResult.PrevalOnly,
			}))
			continue
		}
		// A healed communicate (IsError=false) delivered its message live.
		// Repair the raw bytes with the same RepairJSON machinery the live
		// path used, extract the message, and emit it as a CommunicateData —
		// matching what live delivered and what reload now recovers.
		if commRawArgs != "" {
			repaired := argrepair.RepairJSON([]byte(commRawArgs))
			if msg := apptranscript.CommunicateMessageFromArguments(repaired); msg != "" {
				liveEvents = append(liveEvents, events.New(events.CommunicateData{Message: msg}))
			}
		}
	}
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}
	live := normalizeMetamorphic(foldLiveItems(notes))

	// Reload side: write both turns and drive the hub's full saved-transcript
	// projection. Its shared registry consumes the paired communicate before the
	// close-time flush, exactly as a real client reload does.
	reload := normalizeMetamorphic(projectReloadThroughHub(t, acanon.Turn, rcanon.Turn))

	if eq, a, b := jsonEqItems(t, live, reload); !eq {
		t.Fatalf("live-vs-reload multi-entry metamorphic diverged:\n live  =%s\n reload=%s\n assistant=%s\n result=%s", a, b, acanonBytes, rcanonBytes)
	}
}

// mustSynthesize returns the live event stream for a turn, failing the test if
// the turn kind is unsupported (the multi-entry test exercises kinds that must
// have a live path).
func mustSynthesize(t *testing.T, turn schema.Turn) []events.SessionEvent {
	t.Helper()
	evs, supported := synthesizeLiveEvents(turn, false)
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
	resultJSON := []byte(`{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c6","name":"communicate","content":"invalid","is_error":true,"preval_only":true}}]},"timestamp":"2026-06-01T10:00:01Z"}}`)
	checkLiveVsReloadMultiEntry(t, assistantJSON, resultJSON, rawArgs)
}

// TestHubReplay_RepairedCommunicateLiveVsReload verifies that a healed
// communicate (Arguments={}, RawArguments=malformed, IsError=false result)
// renders the SAME healed message on both live and reload. Live repairs the
// raw bytes and emits EventCommunicate{Message:"hello"}; reload recovers the
// same message by repairing RawArguments with RepairJSON (read-only reuse).
// Both sides emit agentMessage{Text:"hello"}, so the metamorphic passes.
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

	liveEvents, supported := synthesizeLiveEvents(canon.Turn, true)
	if !supported {
		return // turn kind has no clean item-producing live path (see synthesizer)
	}

	// Live side: drive a fresh projector and fold its notifications.
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}
	// Exclude only the known product divergence's extra CallID-scoped live item.
	// Keeping the reload item untouched is load-bearing: the echo-dedup mutation
	// must still redden this oracle by adding the duplicate on reload.
	liveItems := excludeKnownClosingCallIDEchoes(canon.Turn, foldLiveItems(notes))
	live := normalizeMetamorphic(liveItems)

	// Reload side: the hub's own full saved-transcript projection path. Unlike a
	// direct ProjectTurn call, this closes the projection by flushing any
	// communicate whose paired result never reached the transcript.
	reload := normalizeMetamorphic(projectReloadThroughHub(t, canon.Turn))

	if eq, a, b := jsonEqItems(t, live, reload); !eq {
		t.Fatalf("live-vs-reload metamorphic diverged:\n live  =%s\n reload=%s\n entry=%s", a, b, canonBytes)
	}
}

// projectReloadThroughHub persists turns beneath an isolated state directory
// and drives the same full-transcript projection that serves saved hub clients.
// The fixed identity and timestamp keep fuzz seed replay deterministic; the
// unique t.TempDir path keeps the shared transcript cache hermetic.
func projectReloadThroughHub(t *testing.T, turns ...schema.Turn) []appwire.ThreadItem {
	t.Helper()
	const sessionID = "01REPLAYORACLE"
	const openerText = "replay oracle fixed opener"
	stateDir := t.TempDir()
	sessionsDir := filepath.Join(stateDir, "sessions")
	w, err := transcript.NewWriter(filepath.Join(sessionsDir, sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC),
		ProfileID: "replay-oracle",
		Model:     "replay-oracle",
	})
	if err != nil {
		t.Fatalf("create replay transcript: %v", err)
	}
	// A persisted assistant record is a continuation of a user-opened logical
	// turn. Supplying that fixed opener also gives communicate-only assistant
	// records a real turn to which the close-time flush can attach. The opener's
	// item is removed below because the differential owns only the fuzzed turns.
	if err := w.Append(schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.User(openerText),
		Timestamp: time.Date(2026, time.June, 1, 9, 59, 59, 0, time.UTC),
	}); err != nil {
		_ = w.Close()
		t.Fatalf("append replay transcript opener: %v", err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			_ = w.Close()
			t.Fatalf("append replay transcript turn: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close replay transcript: %v", err)
	}

	projected, err := computePastEntryTurns(hubcore.WebConfig{}, hubcore.PastEntry{
		ID:       sessionID,
		Meta:     schema.SessionMeta{ID: sessionID, ProfileID: "replay-oracle", Model: "replay-oracle"},
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("hub full-transcript projection: %v", err)
	}
	if len(projected) == 0 || len(projected[0].Items) == 0 {
		t.Fatal("hub full-transcript projection omitted fixed opener turn")
	}
	opener := projected[0].Items[0]
	if opener.Type != "userMessage" || opener.Text != openerText {
		t.Fatalf("hub full-transcript projection first item is not fixed opener: %+v", opener)
	}
	var items []appwire.ThreadItem
	for turnIndex, turn := range projected {
		start := 0
		if turnIndex == 0 {
			start = 1
		}
		items = append(items, turn.Items[start:]...)
	}
	return items
}

func TestProjectReloadThroughHubDropsOnlyFixedOpener(t *testing.T) {
	const openerText = "replay oracle fixed opener"
	items := projectReloadThroughHub(t, schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.User(openerText),
		Timestamp: time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC),
	})
	if len(items) != 1 || items[0].Type != "userMessage" || items[0].Text != openerText {
		t.Fatalf("expected only the fuzzed user message after dropping the fixed opener, got: %+v", items)
	}
}

func TestProjectReloadThroughHubPreservesPagingAndImageMetadata(t *testing.T) {
	fixture := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID:     "image-call",
				Name:           "screenshot",
				Content:        "captured",
				ImageData:      []byte("hello"),
				ImageMediaType: "image/png",
			}},
		}},
		Timestamp: time.Date(2026, time.June, 1, 10, 0, 0, 0, time.UTC),
	}
	first := projectReloadThroughHub(t, fixture)
	second := projectReloadThroughHub(t, fixture)
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("reload projection item counts differ: first=%d second=%d", len(first), len(second))
	}

	imageCount := 0
	for i := range first {
		if first[i].Position == nil || second[i].Position == nil {
			t.Fatalf("reload item %d omitted paging position: first=%+v second=%+v", i, first[i].Position, second[i].Position)
		}
		if *first[i].Position != *second[i].Position {
			t.Fatalf("reload item %d paging position changed: first=%+v second=%+v", i, *first[i].Position, *second[i].Position)
		}
		if first[i].TranscriptKey == "" || first[i].TranscriptKey != second[i].TranscriptKey {
			t.Fatalf("reload item %d transcript key is empty or unstable: first=%q second=%q", i, first[i].TranscriptKey, second[i].TranscriptKey)
		}
		if len(first[i].OutputImages) != len(second[i].OutputImages) {
			t.Fatalf("reload item %d output image counts differ: first=%d second=%d", i, len(first[i].OutputImages), len(second[i].OutputImages))
		}
		for imageIndex, image := range first[i].OutputImages {
			imageCount++
			if image.SHA == "" {
				t.Fatalf("reload output image omitted content sha: %+v", image)
			}
			wantURL := "/s/01REPLAYORACLE/images/" + image.SHA
			if image.URL != wantURL {
				t.Fatalf("reload output image URL = %q, want %q", image.URL, wantURL)
			}
			if second[i].OutputImages[imageIndex].URL != image.URL {
				t.Fatalf("reload output image URL changed: first=%q second=%q", image.URL, second[i].OutputImages[imageIndex].URL)
			}
		}
	}
	if imageCount != 1 {
		t.Fatalf("reload projection output image count = %d, want 1: %+v", imageCount, first)
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
func synthesizeLiveEvents(turn schema.Turn, transcriptCloses bool) ([]events.SessionEvent, bool) {
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
				// (the live ToolCallStart for communicate is suppressed). Both
				// live and reload defer ALL communicate messages to the paired
				// result turn: the assistant turn alone cannot disambiguate a
				// valid-JSON communicate that will succeed from one rejected at
				// prevalidation (PrevalOnly), nor a malformed communicate that
				// will be healed from one that will be rejected. The result
				// turn carries the IsError/PrevalOnly status that
				// disambiguates. The single-entry metamorphic compares the
				// assistant turn alone, so both sides agree (nothing). The
				// multi-entry test (checkLiveVsReloadMultiEntry) exercises the
				// paired result turn, which carries the error/PrevalOnly status
				// that disambiguates.
				if p.ToolCall.Name == "communicate" {
					if transcriptCloses {
						repaired := argrepair.RepairJSON([]byte(p.ToolCall.SentArguments()))
						normalized := apptranscript.NormalizeCommunicateArguments(repaired)
						if msg := apptranscript.CommunicateMessageFromArguments(normalized); msg != "" {
							// At transcript close there is no paired result left to
							// disambiguate. Model the preview and successful delivery the
							// live client already saw; reload recovers the same message by
							// flushing the unconsumed registry entry.
							add(events.CommunicatePreviewStartData{CallID: p.ToolCall.ID})
							add(events.CommunicateData{CallID: p.ToolCall.ID, Message: msg})
						}
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
			// On reload, a communicate result is skipped here too — the raw
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
		// Full transcript reloads assign stable server paging coordinates;
		// live stream items do not have them. They are identity, not rendered
		// content, so normalize only these two coordinate fields.
		it.Position = nil
		it.TranscriptKey = ""
		// The hub stamps its fetch route only after the reload projector has
		// described output images. Live descriptors intentionally have no URL;
		// both sides still compare source, name, type, size, sha, and path.
		for i := range it.OutputImages {
			it.OutputImages[i].URL = ""
		}

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

// TestHubReplay_UnpairedCommunicateFlushParity pins the exact item shape for the
// unpaired-communicate close-time flush that the fuzz oracle now drives through
// projectReloadThroughHub. The reload side below independently projects with a
// shared registry and flushes it; the live side synthesizes the preview + commit
// pair. Both must render one matching agentMessage after identity normalization.
func TestHubReplay_UnpairedCommunicateFlushParity(t *testing.T) {
	entryJSON := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"call_unpaired","name":"communicate","arguments":{"message":"hello there"}}}]},"timestamp":"2026-06-01T10:00:00Z"}}`)

	var e transcript.Entry
	if err := json.Unmarshal(entryJSON, &e); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	canon, canonBytes := canonicalEntry(t, e)

	// Live side: synthesize the preview + commit the live projector emitted for
	// a delivered communicate. The message is extracted from the call's
	// arguments the same way the flush does (CommunicateMessageFromArguments).
	msg := apptranscript.CommunicateMessageFromArguments(
		canonicalCommunicateArguments(t, canon.Turn),
	)
	if msg == "" {
		t.Fatalf("communicate message extraction returned empty for arguments")
	}
	liveEvents := []events.SessionEvent{
		events.New(events.CommunicatePreviewStartData{CallID: "call_unpaired"}),
		events.New(events.CommunicateData{CallID: "call_unpaired", Message: msg}),
	}
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}
	live := normalizeMetamorphic(foldLiveItems(notes))

	// Reload side: project with a shared registry (the way the server threads
	// it across entries), then flush — the server's appTurnProjectionFromTranscriptFile
	// calls FlushUnpairedCommunicates after groupedAppTurnProjection.
	reconstructed, ok := decodeTranscriptTurn(canonBytes)
	if !ok {
		t.Fatalf("hub decode rejected the canonical entry: %s", canonBytes)
	}
	reg := apptranscript.NewToolCallRegistry()
	items := apptranscript.ProjectTurn("turn_1", 1, reconstructed, reg, nil, apptranscript.ToolResultOutputImages)
	turns := []appwire.Turn{{ID: "turn_1", Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}}
	apptranscript.FlushUnpairedCommunicates(&turns, reg)
	// FlushUnpairedCommunicates positions the flushed items (Position,
	// TranscriptKey), but ProjectTurn alone — the shape checkLiveVsReload
	// and normalizeMetamorphic were designed for — does not. Strip the
	// positioning metadata so the comparison is on rendered content, not
	// on index-derived keys the live side never has.
	for i := range turns[0].Items {
		turns[0].Items[i].Position = nil
		turns[0].Items[i].TranscriptKey = ""
	}
	reload := normalizeMetamorphic(turns[0].Items)

	if eq, a, b := jsonEqItems(t, live, reload); !eq {
		t.Fatalf("unpaired communicate flush parity diverged:\n live  =%s\n reload=%s", a, b)
	}
	if len(live) != 1 || live[0].Type != "agentMessage" || live[0].Text != "hello there" {
		t.Fatalf("expected one agentMessage with text %q, got: %+v", "hello there", live)
	}
}

// canonicalCommunicateArguments extracts the communicate tool call's arguments
// from the assistant turn, so the test can feed the same message the flush
// recovers to the live synthesizer.
func canonicalCommunicateArguments(t *testing.T, turn schema.Turn) json.RawMessage {
	t.Helper()
	for _, p := range turn.Message.Content {
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == "communicate" {
			return p.ToolCall.Arguments
		}
	}
	t.Fatalf("no communicate tool call in turn")
	return nil
}

// TestHubReplay_RuntimeFailedCommunicateLiveVsReload verifies that a
// communicate which executed and failed at runtime (IsError=true,
// PrevalOnly=false — the call ran and returned an error) surfaces NOTHING on
// both live and reload. Reload already pins this (renders nothing for
// PrevalOnly=false); before the fix, live emitted a settledCommunicateFailure
// because it gated on !hadProvisionalItem rather than data.PrevalOnly,
// diverging from reload. The fix aligns live to reload's IsError&&PrevalOnly
// predicate: a runtime failure surfaces nothing on either side.
func TestHubReplay_RuntimeFailedCommunicateLiveVsReload(t *testing.T) {
	const rawArgs = `{message: "hi"}` // malformed JSON — bare key
	escaped := strings.ReplaceAll(rawArgs, `"`, `\"`)
	assistantJSON := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c_rt","name":"communicate","arguments":{},"raw_arguments":"` + escaped + `"}}]},"timestamp":"2026-06-01T10:00:00Z"}}`)
	resultJSON := []byte(`{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c_rt","name":"communicate","content":"runtime error","is_error":true,"preval_only":false}}]},"timestamp":"2026-06-01T10:00:01Z"}}`)
	checkLiveVsReloadMultiEntry(t, assistantJSON, resultJSON, rawArgs)
}

// TestHubReplay_PreviewedPrevalOnlyCommunicateLiveVsReload verifies that a
// PrevalOnly communicate rejection whose preview started (the live projector
// emitted a provisional agentMessage via EventCommunicatePreviewStart, then
// the call was rejected at prevalidation) retracts the provisional message AND
// surfaces the commandExecution error item — matching what reload renders
// from the deferred CommRawArgs. Before the fix, live gated the
// settledCommunicateFailure on !hadProvisionalItem; with a preview started,
// hadProvisionalItem was true, so live emitted only the reset and NOT the
// error item — diverging from reload, which always renders the
// commandExecution error for IsError&&PrevalOnly regardless of preview state.
func TestHubReplay_PreviewedPrevalOnlyCommunicateLiveVsReload(t *testing.T) {
	const rawArgs = `{message: "hi"}` // malformed JSON — bare key
	escaped := strings.ReplaceAll(rawArgs, `"`, `\"`)
	assistantJSON := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c_prev","name":"communicate","arguments":{},"raw_arguments":"` + escaped + `"}}]},"timestamp":"2026-06-01T10:00:00Z"}}`)
	resultJSON := []byte(`{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c_prev","name":"communicate","content":"arguments rejected","is_error":true,"preval_only":true}}]},"timestamp":"2026-06-01T10:00:01Z"}}`)

	var ae, re transcript.Entry
	if err := json.Unmarshal(assistantJSON, &ae); err != nil {
		t.Fatalf("decode assistant entry: %s", assistantJSON)
	}
	if err := json.Unmarshal(resultJSON, &re); err != nil {
		t.Fatalf("decode result entry: %s", resultJSON)
	}
	acanon, acanonBytes := canonicalEntry(t, ae)
	rcanon, rcanonBytes := canonicalEntry(t, re)

	// Live side: synthesize the assistant turn's events, then model the
	// preview-start (the live projector created a provisional agentMessage),
	// the tool-call start (which transitions preview→executing under
	// suppression), and the tool-call end (PrevalOnly=true, Error set).
	var liveEvents []events.SessionEvent
	liveEvents = append(liveEvents, mustSynthesize(t, acanon.Turn)...)
	liveEvents = append(liveEvents,
		events.New(events.CommunicatePreviewStartData{CallID: "c_prev"}),
		events.New(events.ToolCallStartData{ToolName: "communicate", CallID: "c_prev"}),
		events.New(events.ToolCallEndData{
			ToolName:      "communicate",
			CallID:        "c_prev",
			ArgumentsJSON: rawArgs,
			Error:         apptranscript.StringifyToolContent(rcanon.Turn.Message.Content[0].ToolResult.Content),
			PrevalOnly:    true,
		}),
	)
	proj := appprojector.NewAppEventProjector("thread", "local:thread")
	var notes []appprojector.AppNotification
	for _, ev := range liveEvents {
		notes = append(notes, proj.Project(ev)...)
	}

	// The live stream must contain BOTH a NotifyAgentMessageReset (the
	// provisional agentMessage retracted) AND a NotifyItemCompleted whose
	// Item is a commandExecution communicate error (the settled failure).
	var hasReset bool
	var liveItem appwire.ThreadItem
	for _, n := range notes {
		if n.Method == appwire.NotifyAgentMessageReset {
			hasReset = true
		}
		if n.Method == appwire.NotifyItemCompleted {
			if p, ok := n.Params.(appwire.ItemLifecycleParams); ok {
				if p.Item.Type == "commandExecution" && p.Item.ToolName == "communicate" {
					liveItem = p.Item
				}
			}
		}
	}
	if !hasReset {
		t.Fatalf("live must retract the provisional agentMessage (NotifyAgentMessageReset); notes: %+v", notes)
	}
	if liveItem.Type == "" {
		t.Fatalf("live must surface the PrevalOnly communicate rejection as a commandExecution error item (NotifyItemCompleted); notes: %+v", notes)
	}

	// Reload side: project both turns with a shared registry, the way the
	// hub's full-transcript read does.
	reg := apptranscript.NewToolCallRegistry()
	aReconstructed, ok := decodeTranscriptTurn(acanonBytes)
	if !ok {
		t.Fatalf("hub decode rejected assistant entry: %s", acanonBytes)
	}
	rReconstructed, ok := decodeTranscriptTurn(rcanonBytes)
	if !ok {
		t.Fatalf("hub decode rejected result entry: %s", rcanonBytes)
	}
	reloadItems := append(
		apptranscript.ProjectTurn("turn_1", 1, aReconstructed, reg, nil, apptranscript.ToolResultOutputImages),
		apptranscript.ProjectTurn("turn_2", 2, rReconstructed, reg, nil, apptranscript.ToolResultOutputImages)...,
	)
	var reloadItem appwire.ThreadItem
	for _, item := range reloadItems {
		if item.Type == "commandExecution" && item.ToolName == "communicate" {
			reloadItem = item
			break
		}
	}
	if reloadItem.Type == "" {
		t.Fatalf("reload must render the PrevalOnly communicate rejection as a commandExecution error item; items: %+v", reloadItems)
	}

	// Compare live and reload via normalizeMetamorphic (strips identity/
	// status/timing but preserves Type, ToolName, ArgumentsJSON, Error,
	// PrevalOnly — the content that must match).
	liveNorm := normalizeMetamorphic([]appwire.ThreadItem{liveItem})
	reloadNorm := normalizeMetamorphic([]appwire.ThreadItem{reloadItem})
	if len(liveNorm) != 1 {
		t.Fatalf("live normalized = %d items, want 1 (the commandExecution error)", len(liveNorm))
	}
	if eq, a, b := jsonEqItems(t, liveNorm, reloadNorm); !eq {
		t.Fatalf("live-vs-reload previewed-PrevalOnly communicate diverged:\n live  =%s\n reload=%s", a, b)
	}
}
