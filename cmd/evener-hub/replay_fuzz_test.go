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

// checkLiveVsReload runs the live-vs-reload differential on one entry's JSON.
// Shared by the raw-byte target above and the structure-aware target below.
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
