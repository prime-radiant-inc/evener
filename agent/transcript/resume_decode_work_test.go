package transcript

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// resumeDecodeFixtureEntries is how many entries the fixture transcript holds.
// Large enough that a second parse per entry shows up in the counted bytes,
// small enough that building and resuming it stays well inside a test's budget.
const resumeDecodeFixtureEntries = 400

// TestResumeDecodesEachEntryOnce pins the resume scan's decode work: one strict
// parse per entry record, not two.
//
// The measure is the bytes the scan hands a decoder, counted by the decoder
// itself rather than inferred from elapsed time or from an allocation count.
// Resume is the whole-file read that gates a session's restart, and on a real
// transcript DecodeEntry's two parses — the boundary read, then the whole
// entry — were 4.02 s of a 4.25 s scan in the measurement on issue #6418, so a
// second parse is the cost this scan exists to avoid. A wall-clock assertion
// cannot hold that: this host runs at load 38 on 16 cores, where the same scan
// varies by a factor of several.
//
// The scan before this change parsed every entry twice (a boundary read, then
// DecodeEntry's whole-record decode), so it counted just under twice these
// bytes. The assertion is one parse's worth plus the records' framing.
func TestResumeDecodesEachEntryOnce(t *testing.T) {
	t.Parallel()
	path, entryBytes, headerBytes := writeResumeDecodeFixture(t)

	before := resumeDecodedEntryBytes.Load()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	header, entries, _, _, stats, err := scanForResumeCounting(f, "RESUME_DECODE")
	_ = f.Close()
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	decoded := resumeDecodedEntryBytes.Load() - before

	if len(entries) != resumeDecodeFixtureEntries {
		t.Fatalf("resumed %d entries, want %d", len(entries), resumeDecodeFixtureEntries)
	}
	if stats.EntriesDecoded != resumeDecodeFixtureEntries {
		t.Fatalf("decode stats report %d entries decoded, want %d", stats.EntriesDecoded, resumeDecodeFixtureEntries)
	}
	if stats.EntryBytesDecoded != entryBytes {
		t.Fatalf("decode stats report %d entry bytes, want the fixture's %d", stats.EntryBytesDecoded, entryBytes)
	}
	if header.SessionID != "RESUME_DECODE" {
		t.Fatalf("resumed header session = %q, want RESUME_DECODE", header.SessionID)
	}

	// One parse of every entry record, plus the header's own decode. Anything
	// near twice the entry bytes means the record was parsed twice.
	once := entryBytes + headerBytes
	ceiling := once + once/4
	if decoded > ceiling {
		t.Fatalf("resume handed decoders %d bytes for %d entries (%d payload bytes, one parse = %d), want at most %d: "+
			"the scan must parse each record once, and the second parse is what issue #6418 measured at 4 s per resume",
			decoded, len(entries), entryBytes, once, ceiling)
	}
	t.Logf("decoded %d bytes for %d entries: %d payload bytes + %d header, one parse = %d, ceiling %d, ratio %.2fx",
		decoded, len(entries), entryBytes, headerBytes, once, ceiling, float64(decoded)/float64(once))
}

// writeResumeDecodeFixture writes a transcript shaped like a real resume: a
// header, then entries carrying the fields a prefix fold reads. It returns the
// path, the exact payload bytes of the entry records, and the header line's
// bytes.
func writeResumeDecodeFixture(t *testing.T) (path string, entryBytes, headerBytes int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "resume-decode.transcript.jsonl")
	w, err := NewWriter(path, Header{SessionID: "RESUME_DECODE", ProfileID: "p", Model: "m"})
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	payloads := make([]int, 0, resumeDecodeFixtureEntries)
	for i := range resumeDecodeFixtureEntries {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User("entry body for the resume decode fixture"))
		turn.TurnID = "t_" + strconv.Itoa(i)
		turn.ClientMutationID = "cm-" + strconv.Itoa(i)
		before, err := os.Stat(path)
		if err != nil {
			_ = w.Close()
			t.Fatalf("stat fixture: %v", err)
		}
		if _, err := w.Record(turn, RecordOptions{Door: DoorBuffered}); err != nil {
			_ = w.Close()
			t.Fatalf("append entry %d: %v", i, err)
		}
		after, err := os.Stat(path)
		if err != nil {
			_ = w.Close()
			t.Fatalf("stat fixture after append: %v", err)
		}
		// The line is the delta: one record plus its newline framing.
		payloads = append(payloads, int(after.Size()-before.Size()-1))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for i, b := range encoded {
		if b == '\n' {
			headerBytes = int64(i)
			break
		}
	}
	if headerBytes == 0 {
		t.Fatal("fixture has no header line")
	}
	for _, p := range payloads {
		if p <= 0 {
			t.Fatalf("fixture entry payload = %d, want positive", p)
		}
		entryBytes += int64(p)
	}
	return path, entryBytes, headerBytes
}
