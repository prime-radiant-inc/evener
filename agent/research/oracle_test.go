package research

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// writeTranscript writes a minimal valid v2 transcript: header + entries.
func writeTranscript(t *testing.T, dir, sid string, entries []transcript.Entry) string {
	t.Helper()
	sessions := filepath.Join(dir, "projects", "proj", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, sid+".transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(entries) == 0 {
		// Header-only file: still valid for walking.
		return path
	}
	return path
}

func timeNowPlus(seconds int64) time.Time {
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

func TestWalkSessionTranscripts_NewestFirstAndLimit(t *testing.T) {
	dir := t.TempDir()
	a := writeTranscript(t, dir, "aaa", nil)
	b := writeTranscript(t, dir, "bbb", nil)
	if err := os.Chtimes(b, timeNowPlus(0), timeNowPlus(0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, timeNowPlus(-3600), timeNowPlus(-3600)); err != nil {
		t.Fatal(err)
	}
	got, err := walkSessionTranscripts(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SessionID != "bbb" || got[1].SessionID != "aaa" {
		t.Fatalf("got %+v, want bbb then aaa", got)
	}
	limited, err := walkSessionTranscripts(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].SessionID != "bbb" {
		t.Fatalf("limit=1 got %+v", limited)
	}
}

func TestLoadEntries_SkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	headerLine := fmt.Sprintf(`{"kind":"header","format_version":%d}`, transcript.FormatVersion)
	userLine := `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}}`
	badLine := `{not json`
	path := filepath.Join(dir, "s.transcript.jsonl")
	content := headerLine + "\n" + userLine + "\n" + badLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, skipped, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || skipped != 1 {
		t.Fatalf("entries=%d skipped=%d, want 1/1", len(entries), skipped)
	}
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Fatalf("kind = %s", entries[0].Turn.Kind)
	}
}
