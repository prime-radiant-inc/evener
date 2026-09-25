package hub

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
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
// what the user sees LIVE -- the daemon's history, projected from the recorded
// entry by the transcript index and published as history/updated -- against
// what the hub renders on RELOAD (saved bytes → decodeTranscriptTurn →
// ProjectTurn), for one turn.
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
	if !replayedKind(canon.Turn.Kind) {
		return // turn kind has no per-turn item rendering to compare
	}

	// Live side: the daemon's history of a transcript holding this entry.
	path := filepath.Join(t.TempDir(), "thread.transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "thread"})
	if err != nil {
		t.Fatalf("new transcript: %v", err)
	}
	if err := writer.Append(canon.Turn); err != nil {
		_ = writer.Close()
		return // an entry no writer records has no live rendering
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	idx, err := transcriptindex.Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("index the transcript: %v", err)
	}
	defer idx.Close() //nolint:errcheck // a read-only projection
	window, err := idx.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("latest window: %v", err)
	}
	var liveItems []appwire.ThreadItem
	for _, candidate := range window.Candidates {
		if candidate.TurnID != appwire.SystemPreludeTurnID {
			liveItems = append(liveItems, candidate.Item)
		}
	}
	live := normalizeMetamorphic(liveItems)

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

// replayedKind reports the turn kinds whose rendering is a per-turn item list
// on both sides (steering and system turns are not).
func replayedKind(kind schema.TurnKind) bool {
	switch kind {
	case schema.TurnUserInput, schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults, schema.TurnCheckpoint, schema.TurnSummary:
		return true
	default:
		return false
	}
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

		// Identity and per-turn status: the daemon's history keys, positions
		// and versions each item by its entry (the reload side's ProjectTurn
		// assigns none), and item IDs, turn IDs, call IDs, status and timing
		// derive from the transcript's grouping, which a one-entry transcript
		// does not share with the reload side's single synthetic turn. None of
		// these are rendered content.
		it.ID = ""
		it.TurnID = ""
		it.CallID = ""
		it.Status = ""
		it.StartedAt = nil
		it.CompletedAt = nil
		it.TranscriptEntryIndex = 0
		it.TranscriptKey = ""
		it.Position = nil
		it.Version = 0

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
