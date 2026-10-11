package sessionlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entryOfRecordSize builds an entry whose canonical JSON encoding is exactly
// want bytes. ASCII padding keeps the encoding linear (no escaping expansion),
// so the record size is deterministic.
func entryOfRecordSize(t *testing.T, want int) SessionLogEntry {
	t.Helper()
	base, err := json.Marshal(SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success"})
	if err != nil {
		t.Fatalf("marshal base entry: %v", err)
	}
	if want < len(base) {
		t.Fatalf("want %d record bytes, below base %d", want, len(base))
	}
	entry := SessionLogEntry{
		Turn:    1,
		Action:  "assistant",
		Outcome: "success",
		Summary: strings.Repeat("a", want-len(base)),
	}
	got, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal sized entry: %v", err)
	}
	if len(got) != want {
		t.Fatalf("sized entry encodes to %d bytes, want %d", len(got), want)
	}
	return entry
}

// TestSessionLog_LargeRecordRoundTrips repeats the audit's own probe: a record
// larger than bufio.Scanner's former 64 KiB default ceiling must be readable
// after the log is reopened. Before the fix, Append accepted the 70,000-byte
// record and reopening then failed with "bufio.Scanner: token too long", so the
// whole log was unusable.
func TestSessionLog_LargeRecordRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)
	entry := entryOfRecordSize(t, 70_000)
	if err := log.Append(entry); err != nil {
		t.Fatalf("Append(large record): %v", err)
	}

	entries := mustNewSessionLog(t, path).Entries()
	if len(entries) != 1 || entries[0].Summary != entry.Summary {
		t.Fatalf("70,000-byte record did not round-trip (entries=%d)", len(entries))
	}
}

// TestSessionLog_RecordSizeBoundary pins the limit at just-below (the audit's
// 70 KB probe is covered above), exact, and just-above: every record Append
// accepts must read back after reopening.
func TestSessionLog_RecordSizeBoundary(t *testing.T) {
	t.Parallel()

	for _, size := range []int{maxRecordBytes - 1, maxRecordBytes} {
		t.Run(fmt.Sprintf("record of %d bytes", size), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "session.log")
			log := mustNewSessionLog(t, path)

			entry := entryOfRecordSize(t, size)
			if err := log.Append(entry); err != nil {
				t.Fatalf("Append(%d-byte record): %v", size, err)
			}
			entries := mustNewSessionLog(t, path).Entries()
			if len(entries) != 1 || entries[0].Summary != entry.Summary {
				t.Fatalf("record of %d bytes did not round-trip (entries=%d)", size, len(entries))
			}
		})
	}

	t.Run("over limit refused without mutation", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "session.log")
		log := mustNewSessionLog(t, path)

		if err := log.Append(entryOfRecordSize(t, 128)); err != nil {
			t.Fatalf("Append(good): %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read persisted log: %v", err)
		}

		err = log.Append(entryOfRecordSize(t, maxRecordBytes+1))
		if err == nil {
			t.Fatal("Append(over limit) = nil error, want a size-limit refusal")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Fatalf("Append(over limit) error = %q, want a size-limit refusal", err)
		}
		if got := log.Len(); got != 1 {
			t.Fatalf("live Len() = %d after refusal, want 1", got)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read persisted log after refusal: %v", err)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("persisted log changed after refused append: before=%d bytes after=%d bytes", len(before), len(after))
		}

		// A refusal must not poison the log: a later valid append still lands.
		if err := log.Append(entryOfRecordSize(t, 128)); err != nil {
			t.Fatalf("Append after refusal: %v", err)
		}
		if got := mustNewSessionLog(t, path).Len(); got != 2 {
			t.Fatalf("reopened Len() = %d, want 2", got)
		}
	})
}
