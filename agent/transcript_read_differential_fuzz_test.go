package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/fuzz/schemagen"
)

// The serf daemon writes one transcript JSONL format, but THREE independently
// written readers consume it:
//
//   - readTranscript            (transcript_read.go) — lenient; bufio.Scanner;
//     returns header + entries + a skipped count; silently drops non-entry lines.
//   - readTranscriptFull        (transcript_read.go) — lenient; bufio.Scanner;
//     peeks "kind" and routes entries vs api_calls into separate slices.
//   - readStrictChildTranscript (transcript_read.go) — STRICT; a hand-rolled
//     bufio.Reader + ReadSlice loop with its own size limit, session pinning,
//     and final-incomplete-line handling; errors (not skips) on any corruption.
//
// These three share NO code: each is its own scanner loop with its own header
// parse, empty-line handling, and kind dispatch. On a well-formed transcript
// they MUST agree on the header and the decoded entries (and the two full
// readers on api_calls). Nothing currently cross-checks them — the existing
// FuzzTranscriptReplay{,Structured} targets exercise the round-trip and call all
// three, but never assert their outputs are equal to one another. That gap is
// exactly where a header-parse, kind-dispatch, or line-framing change in one
// reader would silently drift from the others. This is that differential.

// FuzzTranscriptReadersAgree is a differential oracle over the three transcript
// readers. It drives the structure-aware generator (the same one
// FuzzTranscriptReplayStructured uses) to synthesize a valid-but-adversarial
// transcript — header line of kind "header" followed only by kind entry/api_call
// lines — then asserts:
//
//   - Gate A (whenever the lenient full reader accepts the file): readTranscript
//     and readTranscriptFull return the SAME header and the SAME entries. Both
//     are lenient, so both accept; a divergence here is a header-parse or
//     entry-dispatch drift between the two scanner loops.
//   - Gate B (additionally, when the full reader reports zero skipped lines, i.e.
//     every line is a clean entry/api_call): the strict child reader — pinned to
//     the header's own session id — must also SUCCEED with zero skipped and
//     return the SAME header, entries, AND api_calls. The strict reader's accept
//     domain is exactly "valid header + only known clean kinds + session match +
//     within size limit", which full.Skipped==0 over generator output satisfies
//     by construction; a strict error or a content difference there is a real
//     reader divergence, not expected lenient-vs-strict behavior.
//
// ALLOW-LIST — differences deliberately NOT treated as divergence:
//   - skipped counts between readTranscript and readTranscriptFull: the two
//     readers handle a *malformed api_call line* differently (readTranscript
//     unmarshals the whole line into Entry, which succeeds and is dropped as
//     non-entry without counting; readTranscriptFull peeks the kind then fails
//     the typed APICall unmarshal and counts it). That is a legitimate
//     responsibility difference, so only entries (their shared responsibility)
//     are compared between the two lenient readers.
//   - api_calls are compared only between readTranscriptFull and the strict
//     reader; readTranscript does not collect api_calls at all (by design).
//   - the strict reader is compared only under Gate B; outside it (a generated
//     line that fails a typed decode) the strict reader legitimately ERRORS
//     where the lenient readers skip — that is the documented strict/lenient
//     contract, not a bug.
func FuzzTranscriptReadersAgree(f *testing.F) {
	for _, seed := range transcriptStructuredSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := generateTranscript(schemagen.NewByteSource(data))
		if err != nil {
			t.Fatalf("generateTranscript: %v", err)
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "t.jsonl")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}

		full, fullErr := readTranscriptFull(path)
		if fullErr != nil {
			return // no valid header line: outside the agreement domain
		}

		// Gate A: the lenient scanner readers must agree on header + entries.
		lh, lentries, _, lerr := readTranscript(path)
		if lerr != nil {
			t.Fatalf("readTranscriptFull accepted the file but readTranscript rejected it: %v\n  transcript=%s", lerr, raw)
		}
		assertReaderHeadersEqual(t, "readTranscript", lh, "readTranscriptFull", full.Header, raw)
		assertReaderEntriesEqual(t, "readTranscript", lentries, "readTranscriptFull", full.Entries, raw)

		// Gate B: when every line decoded cleanly, the strict reader must also
		// accept and agree on header + entries + api_calls.
		if full.Skipped != 0 {
			return
		}
		strict, strictErr := readStrictChildTranscript(path, full.Header.SessionID, transcriptJSONLMaxLineBytes)
		if strictErr != nil {
			t.Fatalf("full reader saw a clean transcript (0 skipped) but strict reader rejected it: %v\n  transcript=%s", strictErr, raw)
		}
		if strict.Skipped != 0 {
			t.Fatalf("strict reader skipped %d clean lines that the full reader accepted\n  transcript=%s", strict.Skipped, raw)
		}
		assertReaderHeadersEqual(t, "readTranscriptFull", full.Header, "strict", strict.Header, raw)
		assertReaderEntriesEqual(t, "readTranscriptFull", full.Entries, "strict", strict.Entries, raw)
		assertReaderAPICallsEqual(t, "readTranscriptFull", full.APICalls, "strict", strict.APICalls, raw)
	})
}

func assertReaderHeadersEqual(t *testing.T, aName string, a transcript.Header, bName string, b transcript.Header, raw []byte) {
	t.Helper()
	if eq, ab, bb := jsonEqual(t, a, b); !eq {
		t.Fatalf("transcript header differs between %s and %s:\n  %s=%s\n  %s=%s\n  transcript=%s",
			aName, bName, aName, ab, bName, bb, raw)
	}
}

func assertReaderEntriesEqual(t *testing.T, aName string, a []transcript.Entry, bName string, b []transcript.Entry, raw []byte) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("transcript entry count differs: %s=%d %s=%d\n  transcript=%s", aName, len(a), bName, len(b), raw)
	}
	if eq, ab, bb := jsonEqual(t, turnsOf(a), turnsOf(b)); !eq {
		t.Fatalf("transcript entries differ between %s and %s:\n  %s=%s\n  %s=%s\n  transcript=%s",
			aName, bName, aName, ab, bName, bb, raw)
	}
}

func assertReaderAPICallsEqual(t *testing.T, aName string, a []transcript.APICall, bName string, b []transcript.APICall, raw []byte) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("transcript api_call count differs: %s=%d %s=%d\n  transcript=%s", aName, len(a), bName, len(b), raw)
	}
	if eq, ab, bb := jsonEqual(t, a, b); !eq {
		t.Fatalf("transcript api_calls differ between %s and %s:\n  %s=%s\n  %s=%s\n  transcript=%s",
			aName, bName, aName, ab, bName, bb, raw)
	}
}

// TestTranscriptReadersAgreeSanity is a fast, explicit seed check (no fuzzing):
// a fixed transcript with a header, every turn kind, and an api_call must read
// back identically through all three readers. It documents the oracle's intent
// and guards the readers independently of the fuzz engine.
func TestTranscriptReadersAgreeSanity(t *testing.T) {
	const tx = `{"kind":"header","format_version":1,"session_id":"sane-1","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]},"timestamp":"2026-06-01T10:00:00Z"}}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}
{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"out","is_error":false}}]},"timestamp":"2026-06-01T10:00:02Z"}}
{"kind":"api_call","seq":3,"round":0,"ts":"2026-06-01T10:00:03Z","latency_ms":120,"system_prompt":"you are","request":{},"response":{}}`

	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte(tx), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	full, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	lh, lentries, lskipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	strict, err := readStrictChildTranscript(path, full.Header.SessionID, transcriptJSONLMaxLineBytes)
	if err != nil {
		t.Fatalf("readStrictChildTranscript: %v", err)
	}

	if lskipped != 0 || full.Skipped != 0 || strict.Skipped != 0 {
		t.Fatalf("clean transcript reported skips: readTranscript=%d full=%d strict=%d", lskipped, full.Skipped, strict.Skipped)
	}
	if len(full.Entries) != 3 || len(full.APICalls) != 1 {
		t.Fatalf("expected 3 entries + 1 api_call, got entries=%d api_calls=%d", len(full.Entries), len(full.APICalls))
	}
	assertReaderHeadersEqual(t, "readTranscript", lh, "readTranscriptFull", full.Header, []byte(tx))
	assertReaderHeadersEqual(t, "readTranscriptFull", full.Header, "strict", strict.Header, []byte(tx))
	assertReaderEntriesEqual(t, "readTranscript", lentries, "readTranscriptFull", full.Entries, []byte(tx))
	assertReaderEntriesEqual(t, "readTranscriptFull", full.Entries, "strict", strict.Entries, []byte(tx))
	assertReaderAPICallsEqual(t, "readTranscriptFull", full.APICalls, "strict", strict.APICalls, []byte(tx))
}
