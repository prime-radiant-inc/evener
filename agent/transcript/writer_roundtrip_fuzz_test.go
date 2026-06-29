package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// FuzzTranscriptWriterRoundTrip drives this package's own write + reopen seam:
// NewWriter → Append → Close → OpenWriter (the line-scanning seq-recovery parse
// at transcript.go's json.Unmarshal of each Entry) → Append → Close. Input is a
// JSON array of schema.Turn payloads. Beyond no-panic it asserts the invariant
// the reopen path exists to guarantee: every persisted entry decodes and the seq
// numbers are strictly increasing and collision-free across the reopen boundary
// ("resumed writes never collide with existing entries").
func FuzzTranscriptWriterRoundTrip(f *testing.F) {
	seeds := []string{
		`[{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]},"timestamp":"2026-06-01T10:00:00Z"}]`,
		`[{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hello"},{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]},"timestamp":"2026-06-01T10:00:01Z"}]`,
		`[{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"a"}]}},{"kind":"SUMMARY","message":{"role":"assistant","content":[{"kind":"text","text":"sum"}]}}]`,
		`[]`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var turns []schema.Turn
		if err := json.Unmarshal(raw, &turns); err != nil {
			return // not a turn array: no-panic floor proven, stop
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "transcript.jsonl")
		header := Header{Kind: "header", FormatVersion: 1, SessionID: "fuzz", CreatedAt: time.Unix(0, 0).UTC()}

		w, err := NewWriter(path, header)
		if err != nil {
			t.Fatalf("new writer: %v", err)
		}
		for _, turn := range turns {
			if err := w.Append(turn); err != nil {
				t.Fatalf("append turn: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}

		// Reopen drives the json.Unmarshal seq-recovery scan, then append once
		// more so the recovered seq is exercised against a real subsequent write.
		w2, err := OpenWriter(path)
		if err != nil {
			t.Fatalf("reopen writer: %v", err)
		}
		if err := w2.Append(schema.Turn{Kind: schema.TurnUserInput}); err != nil {
			t.Fatalf("append after reopen: %v", err)
		}
		if err := w2.Close(); err != nil {
			t.Fatalf("close reopened writer: %v", err)
		}

		assertSeqsStrictlyIncreasing(t, path)
	})
}

// assertSeqsStrictlyIncreasing scans every non-header line, confirms it decodes
// as an Entry, and that seq numbers form a strictly increasing (collision-free)
// sequence — the durability invariant OpenWriter's seq recovery upholds.
func assertSeqsStrictlyIncreasing(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back transcript: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	first := true
	prevSeq := -1
	for scanner.Scan() {
		if first {
			first = false
			continue // header
		}
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("persisted entry does not decode: %v\n line=%s", err, scanner.Bytes())
		}
		if e.Seq <= prevSeq {
			t.Fatalf("seq not strictly increasing: got %d after %d", e.Seq, prevSeq)
		}
		prevSeq = e.Seq
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
}
