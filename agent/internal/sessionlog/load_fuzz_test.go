package sessionlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// FuzzSessionLogLoad drives loadFromDisk via NewSessionLog — the package's real
// JSONL decode seam (per-line json.Unmarshal of SessionLogEntry, with malformed
// lines skipped to tolerate partial writes). Input is a raw log file. Beyond
// no-panic it asserts a load→append→reload fixed point: re-appending the loaded
// entries to a fresh log and reloading yields the identical entry slice — proof
// the skip-malformed decode keeps only well-formed, re-serializable entries.
func FuzzSessionLogLoad(f *testing.F) {
	seeds := []string{
		`{"turn":1,"action":"shell","summary":"ran ls","outcome":"success","files_touched":["a.go"]}
{"kind":"advisory","turn":2,"action":"assistant","summary":"thinking","outcome":"success"}
{"turn":3,"action":"edit_file","summary":"fix","outcome":"failure","failures":["nope"]}`,
		`not json
{"turn":1,"action":"x","summary":"y","outcome":"success"}
`,
		``,
		`{}`,
		`{"turn":1}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sessionlog.jsonl")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		log, err := NewSessionLog(path)
		if err != nil {
			return // I/O error on the scan: no-panic floor proven, stop
		}
		entries := log.Entries()

		// Re-append into a fresh log and reload: a fixed point.
		path2 := filepath.Join(dir, "sessionlog2.jsonl")
		log2, err := NewSessionLog(path2)
		if err != nil {
			t.Fatalf("new fresh log: %v", err)
		}
		for _, e := range entries {
			if err := log2.Append(e); err != nil {
				t.Fatalf("append entry: %v", err)
			}
		}
		reloaded, err := NewSessionLog(path2)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}

		got, want := mustMarshal(t, reloaded.Entries()), mustMarshal(t, entries)
		if !bytes.Equal(got, want) {
			t.Fatalf("session log load round-trip diverged:\n want=%s\n got =%s", want, got)
		}
	})
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
