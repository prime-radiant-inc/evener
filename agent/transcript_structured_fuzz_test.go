//go:build evenerfuzz

package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/fuzz/schemagen"
)

// transcriptStructuredSeeds steer the generator across its branches from the
// first bytes; the empty seed exercises the exhaustion-default path (header
// only).
var transcriptStructuredSeeds = [][]byte{
	{},
	{0x00},
	{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	bytes.Repeat([]byte{0xff}, 32),
	[]byte("structured-but-adversarial-transcript-seed"),
	{0x02, 0x02, 0x01, 0x05, 0x02, 0x07, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
	// Found by search: deterministically drives torn_tail=true (issue #156)
	// with one clean entry ahead of the torn one (header decodes, Entries==1,
	// Skipped==1), so a seed-only run (no -fuzz) exercises Gate A of
	// FuzzTranscriptReadersAgree against a torn tail without waiting on the
	// fuzzer to find one.
	{
		0x4f, 0x8e, 0x01, 0x15, 0xc4, 0xfe, 0x47, 0xd1, 0x3f, 0x11,
		0xe2, 0xcf, 0x2b, 0x75, 0x64, 0x1e, 0x48, 0xd4, 0x82, 0xea,
		0xc4, 0xe2, 0x27, 0x04, 0x42, 0x8e, 0x9c, 0x13, 0x21, 0x09,
	},
}

// FuzzTranscriptReplayStructured is roadmap lane 8.4: a structure-aware sibling of
// FuzzTranscriptReplay. It consumes fuzz bytes through generateTranscript to
// synthesize a valid-but-adversarial transcript, then drives it through the REAL
// readTranscriptFull / write-read / ResumeHistory / strict-child-reader path and
// asserts the IDENTICAL oracles as the raw-byte target: never panic, write→read
// round-trip fixed point and ResumeHistory idempotence.
// Because the transcripts are structurally valid, this target reaches the entry
// decoders, orphan-repair, and compaction-scan logic that random bytes almost
// never construct — see TestTranscriptGenReachesDeeper for the gap. A
// failure here on a valid transcript is a real bug, not a test artifact.
func FuzzTranscriptReplayStructured(f *testing.F) {
	for _, seed := range transcriptStructuredSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := generateTranscript(schemagen.NewByteSource(data))
		if err != nil {
			// A transcript that won't even marshal is a generator defect.
			t.Fatalf("generateTranscript: %v", err)
		}

		dir := t.TempDir()
		inPath := filepath.Join(dir, "in.jsonl")
		if err := os.WriteFile(inPath, raw, 0o644); err != nil {
			t.Fatalf("write input transcript: %v", err)
		}
		d, err := readTranscriptFull(inPath)
		if err != nil {
			return // no header / unreadable: no-panic floor proven, stop
		}

		assertTranscriptWriteReadRoundTrip(t, dir, d)
		assertResumeHistoryIdempotent(t, d.Entries)

		sid := d.Header.SessionID
		_, _ = readStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = validateStrictChildTranscript(inPath, sid, transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid+"_mismatch", transcriptJSONLMaxLineBytes)
		_, _ = readStrictChildTranscript(inPath, sid, 4)
	})
}
