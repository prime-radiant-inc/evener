package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/apptranscript"
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
