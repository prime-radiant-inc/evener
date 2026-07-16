//go:build serffuzz

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/fuzz/schemagen"
)

// FuzzTranscriptReadersAgree checks that every semantic v2 reader returns the
// same header and entries for inputs accepted by the shared strict decoder.
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
		// accept and agree on header and entries.
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

// TestTranscriptReadersAgreeSanity is a fast, explicit seed check (no fuzzing):
// a fixed transcript with a header and every turn kind must read
// back identically through all three readers. It documents the oracle's intent
// and guards the readers independently of the fuzz engine.
func TestTranscriptReadersAgreeSanity(t *testing.T) {
	const tx = `{"kind":"header","format_version":2,"session_id":"sane-1","created_at":"2026-06-01T10:00:00Z","profile_id":"openai","model":"gpt-5.5"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]},"timestamp":"2026-06-01T10:00:00Z"}}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:01Z"}}
{"kind":"entry","seq":2,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"out","is_error":false}}]},"timestamp":"2026-06-01T10:00:02Z"}}`

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
	if len(full.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(full.Entries))
	}
	assertReaderHeadersEqual(t, "readTranscript", lh, "readTranscriptFull", full.Header, []byte(tx))
	assertReaderHeadersEqual(t, "readTranscriptFull", full.Header, "strict", strict.Header, []byte(tx))
	assertReaderEntriesEqual(t, "readTranscript", lentries, "readTranscriptFull", full.Entries, []byte(tx))
	assertReaderEntriesEqual(t, "readTranscriptFull", full.Entries, "strict", strict.Entries, []byte(tx))
}
