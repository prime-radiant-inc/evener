package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
)

// turnsOf extracts the Turn payloads from transcript entries.
func turnsOf(entries []transcript.Entry) []schema.Turn {
	turns := make([]schema.Turn, len(entries))
	for i, e := range entries {
		turns[i] = e.Turn
	}
	return turns
}

// entriesOf wraps turns back into transcript entries, so a resumed history can be
// fed through ResumeHistory a second time for the idempotence check.
func entriesOf(turns []schema.Turn) []transcript.Entry {
	entries := make([]transcript.Entry, len(turns))
	for i, turn := range turns {
		entries[i] = transcript.Entry{Kind: "entry", Seq: i, Turn: turn}
	}
	return entries
}

// jsonEqual reports whether a and b marshal to identical JSON. schema.Turn
// carries `any` (ToolResult.Content) and json.RawMessage fields, so a JSON
// compare (not reflect.DeepEqual) is the right equality after a write/read or
// resume round-trip; both sides are post-decode values.
func jsonEqual(t *testing.T, a, b any) (bool, []byte, []byte) {
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

// FuzzTranscriptReplay drives the transcript write/read and resume-replay seam
// (transcript.Writer + readTranscript/readTranscriptFull + ResumeHistory). Input
// is a transcript JSONL blob. Beyond no-panic it asserts:
//
//   - Write→read round-trip: every Turn survives the real Append + read path
//     (the daemon writes turns; the hub/resume reads them back). A dropped or
//     mis-tagged content field shows up as a turn divergence.
//   - ResumeHistory idempotence: re-resuming a resumed history is a fixed point.
//     A compaction-scan or orphan-repair regression breaks it.
//   - APICall round-trip fixed point: api_call lines survive AppendAPICall + read
//     (Seq stripped, since AppendAPICall reassigns it from the writer counter).
func FuzzTranscriptReplay(f *testing.F) {
	seeds := []string{
		// Header + user + assistant(thinking/text/tool_call) + tool_results + api_call.
		`{"kind":"header","format_version":1,"session_id":"s1","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]},"timestamp":"2026-06-01T10:00:00Z"}}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"thinking","thinking":{"text":"hmm"}},{"kind":"text","text":"hi"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}
{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"out","is_error":false}}]},"timestamp":"2026-06-01T10:00:02Z"}}
{"kind":"api_call","seq":3,"round":0,"ts":"2026-06-01T10:00:03Z","latency_ms":120,"system_prompt":"you are","request":{},"response":{}}`,
		// Compaction turn (SUMMARY) exercises the ResumeHistory compaction branch.
		`{"kind":"header","format_version":1,"session_id":"s2","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"old"}]},"timestamp":"2026-06-01T10:00:00Z"}}
{"kind":"entry","seq":1,"turn":{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"summary so far"}]},"timestamp":"2026-06-01T10:00:01Z"}}
{"kind":"entry","seq":2,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"new"}]},"timestamp":"2026-06-01T10:00:02Z"}}`,
		// Orphaned tool result (no preceding tool_call) exercises orphan repair.
		`{"kind":"header","format_version":1,"session_id":"s3","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"orphan","content":"dangling"}}]},"timestamp":"2026-06-01T10:00:00Z"}}`,
		// Orphaned tool CALL (assistant tool_call with no following tool_result)
		// forces ResumeHistory to insert a synthetic result (repairs > 0), which
		// exercises the post-repair re-scan invariant.
		`{"kind":"header","format_version":1,"session_id":"s5","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"go"}]},"timestamp":"2026-06-01T10:00:00Z"}}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c9","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}`,
		// Header only.
		`{"kind":"header","format_version":1,"session_id":"s4","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}`,
		`not a transcript`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		inPath := filepath.Join(dir, "in.jsonl")
		if err := os.WriteFile(inPath, raw, 0o644); err != nil {
			t.Fatalf("write input transcript: %v", err)
		}
		data, err := readTranscriptFull(inPath)
		if err != nil {
			return // no header / unreadable: no-panic floor proven, stop
		}

		assertTranscriptWriteReadRoundTrip(t, dir, data)
		assertResumeHistoryIdempotent(t, data.Entries)
		assertAPICallRoundTrip(t, dir, data)

		// Also drive the strict child-transcript reader/validator — a separate,
		// size-bounded, session-pinned decode seam the round-trip path skips. Hit
		// the session-match success, the session-mismatch rejection, and the
		// per-line size-limit branch, none of which may panic.
		sid := data.Header.SessionID
		_, _ = readStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = validateStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid+"_mismatch", transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid, 4)
	})
}

func assertTranscriptWriteReadRoundTrip(t *testing.T, dir string, data transcriptData) {
	t.Helper()
	out := filepath.Join(dir, "out.jsonl")
	w, err := transcript.NewWriter(out, data.Header)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for _, e := range data.Entries {
		if err := w.Append(e.Turn); err != nil {
			t.Fatalf("append turn: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	_, got, _, err := readTranscript(out)
	if err != nil {
		t.Fatalf("re-read transcript: %v", err)
	}
	if eq, a, b := jsonEqual(t, turnsOf(data.Entries), turnsOf(got)); !eq {
		t.Fatalf("transcript write/read round-trip diverged:\n in =%s\n out=%s", a, b)
	}
}

func assertResumeHistoryIdempotent(t *testing.T, entries []transcript.Entry) {
	t.Helper()
	h1 := ResumeHistory(entries)
	h2 := ResumeHistory(entriesOf(h1))
	if eq, a, b := jsonEqual(t, h1, h2); !eq {
		t.Fatalf("ResumeHistory is not idempotent:\n once =%s\n twice=%s", a, b)
	}
}

// assertAPICallRoundTrip checks the api_call persistence fidelity. AppendAPICall
// reassigns Kind/Seq from the writer counter, so the re-read seqs are 1..N in
// write order, not the originals; Seq is zeroed on both sides before compare.
func assertAPICallRoundTrip(t *testing.T, dir string, data transcriptData) {
	t.Helper()
	if len(data.APICalls) == 0 {
		return
	}
	out := filepath.Join(dir, "api.jsonl")
	w, err := transcript.NewWriter(out, data.Header)
	if err != nil {
		t.Fatalf("new api writer: %v", err)
	}
	for _, c := range data.APICalls {
		if err := w.AppendAPICall(c); err != nil {
			t.Fatalf("append api_call: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close api writer: %v", err)
	}
	reread, err := readTranscriptFull(out)
	if err != nil {
		t.Fatalf("re-read api transcript: %v", err)
	}
	want := normalizeAPICallSeq(data.APICalls)
	got := normalizeAPICallSeq(reread.APICalls)
	if eq, a, b := jsonEqual(t, want, got); !eq {
		t.Fatalf("api_call round-trip fixed point diverged:\n in =%s\n out=%s", a, b)
	}
}

func normalizeAPICallSeq(calls []transcript.APICall) []transcript.APICall {
	out := append([]transcript.APICall(nil), calls...)
	for i := range out {
		out[i].Seq = 0
	}
	return out
}
