//go:build evenerfuzz

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/fuzz/schemagen"
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
