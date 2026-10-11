package hub

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// replayFuzzSeeds are real assistant/user/tool turns covering each content kind,
// shared by the live-vs-reload fuzz targets, which record each in the new
// format. Each is the JSON of one transcript
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

// FuzzHubReplayLiveVsReload is the live-vs-reload differential of the history
// every client renders. LIVE is the daemon's transcript index extended entry
// by entry as each entry is recorded -- what history/updated publishes. RELOAD
// is a fresh index built over the whole file in one pass -- what a read, or a
// hub serving the session daemonless, answers from. The fuzzed entry is
// recorded in the new format, inside an execution, and both indexes are walked
// through every page: the two must hold the same items and turns, at the same
// positions and versions. A projection rule that depends on how the index
// got to an entry (its incremental state) rather than on the entries
// themselves diverges here.
func FuzzHubReplayLiveVsReload(f *testing.F) {
	for _, s := range replayFuzzSeeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		checkLiveVsReload(t, raw)
	})
}

// replayExecution is the execution the fuzzed entry is recorded in.
const replayExecution = "turn_m1"

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
func checkLiveVsReload(t *testing.T, raw []byte) {
	t.Helper()
	var e transcript.Entry
	if json.Unmarshal(raw, &e) != nil || e.Turn.Kind == "" {
		return
	}
	canon, _ := canonicalEntry(t, e)
	fuzzed := inReplayExecution(canon.Turn)
	fuzzed.TurnKind = ""
	opener := inReplayExecution(schema.NewTurn(schema.TurnUserInput, llm.User("replay")))
	opener.TurnKind = schema.TurnSpanExecution
	completion := inReplayExecution(schema.Turn{
		Kind:       schema.TurnCompletion,
		Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted},
	})

	path := filepath.Join(t.TempDir(), "thread.transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "thread"})
	if err != nil {
		t.Fatalf("new transcript: %v", err)
	}
	defer writer.Close() //nolint:errcheck // closed below; this covers the early returns
	live, err := transcriptindex.Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("index the header: %v", err)
	}
	defer live.Close() //nolint:errcheck // a read-only projection
	for _, turn := range []schema.Turn{opener, fuzzed, completion} {
		if err := writer.Append(turn); err != nil {
			return // an entry no writer records has no rendering
		}
		if err := live.CatchUp(); err != nil {
			t.Fatalf("extend the live index over %s: %v", turn.Kind, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	reload, err := transcriptindex.Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("build the reload index: %v", err)
	}
	defer reload.Close() //nolint:errcheck // a read-only projection

	liveHistory, reloadHistory := walkReplayHistory(t, live), walkReplayHistory(t, reload)
	if !bytes.Equal(liveHistory, reloadHistory) {
		t.Fatalf("live-vs-reload history diverged:\n live  =%s\n reload=%s\n entry=%s", liveHistory, reloadHistory, raw)
	}
}

// inReplayExecution stamps turn as a new-format entry of replayExecution.
func inReplayExecution(turn schema.Turn) schema.Turn {
	turn.Format = schema.TurnFormatIdentity
	turn.TurnID = replayExecution
	return turn
}

// replayPage is small so the walk crosses page boundaries.
const replayPage = 2

// walkReplayHistory is every candidate of idx, oldest first, walked from the
// latest page through every older one, encoded for comparison. The window's
// incarnation is the index's own and is left out.
func walkReplayHistory(t *testing.T, idx *transcriptindex.Index) []byte {
	t.Helper()
	window, err := idx.Latest(replayPage)
	if err != nil {
		t.Fatalf("latest page: %v", err)
	}
	candidates := window.Candidates
	length := window.Length
	for window.HasOlder {
		if window, err = idx.Before(window.Candidates[0].Position, replayPage); err != nil {
			t.Fatalf("older page: %v", err)
		}
		candidates = append(append([]appitempaging.TranscriptItemCandidate(nil), window.Candidates...), candidates...)
	}
	encoded, err := json.Marshal(struct {
		Length     int64
		Candidates []appitempaging.TranscriptItemCandidate
	}{length, candidates})
	if err != nil {
		t.Fatalf("encode history: %v", err)
	}
	return encoded
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
