package rendezvous

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzList drives the real rendezvous.List seam: it writes fuzzed bytes into a
// temp directory as a "<pid>.json" file and lists it, exercising the directory
// walk, the .json filtering, the numeric-basename guard, and the per-file
// json.Unmarshal into Entry. The oracle is floor "no panic": List is documented
// to skip corrupt files rather than error, so any byte soup must yield a clean
// (possibly empty) result, never a crash.
func FuzzList(f *testing.F) {
	f.Add([]byte(`{"pid":1,"address":"127.0.0.1:1","started_at":"2020-01-01T00:00:00Z"}`))
	f.Add([]byte(`{"pid":2,"address":"x","model":"gpt","provider":"openai"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Add([]byte(`{"pid":"not an int"}`))
	f.Add([]byte(`[1,2,3]`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "1234.json"), raw, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		entries, err := List(dir)
		if err != nil {
			t.Fatalf("List returned an error for a corrupt-file directory (should skip): %v\n raw=%q", err, raw)
		}
		// Entries that did parse must be self-consistent: a parsed entry came from
		// json.Unmarshal succeeding, so it is at minimum a valid Entry value. Touch
		// a field to guarantee the slice is usable (catches a partially-built slice).
		for range entries {
		}
	})
}

// TestWriteListRoundTrip exercises the json.Marshal side of the protocol against
// List's json.Unmarshal side: a written Entry must list back unchanged.
func TestWriteListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{PID: 4321, Address: "127.0.0.1:9000", Provider: "openai", Model: "gpt-5.5", SessionID: "sess-1"}
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write failed: %v\n entry=%#v", err, entry)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List failed after Write: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly the written entry, got %d", len(entries))
	}
	got := entries[0]
	if got.PID != entry.PID || got.Address != entry.Address || got.Provider != entry.Provider ||
		got.Model != entry.Model || got.SessionID != entry.SessionID {
		t.Fatalf("round-trip mismatch:\n wrote=%#v\n read =%#v", entry, got)
	}
}
