package rendezvous

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWrite_CreatesFileWithExpectedShape(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{
		PID:        12345,
		Address:    "127.0.0.1:54321",
		WorkingDir: "/tmp/example",
		StateDir:   "/tmp/state",
		Agent:      "default",
		Model:      "gpt-5.2",
		Provider:   "openai",
		StartedAt:  time.Date(2026, 5, 7, 14, 32, 11, 0, time.UTC),
		SpawnedBy:  "user",
	}
	path, err := Write(dir, entry)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := filepath.Join(dir, "12345.json")
	if path != want {
		t.Fatalf("path: got %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != entry {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", got, entry)
	}
}

func TestRemove_TolerantOfMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := Remove(dir, 99999); err != nil {
		t.Fatalf("Remove on missing file: %v", err)
	}
}

func TestRemove_DeletesExistingFile(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{PID: 12345, Address: "127.0.0.1:1"}
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Remove(dir, 12345); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "12345.json")); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, got err=%v", err)
	}
}

func TestList_ReturnsAllEntries(t *testing.T) {
	dir := t.TempDir()
	e1 := Entry{PID: 1, Address: "127.0.0.1:1"}
	e2 := Entry{PID: 2, Address: "127.0.0.1:2"}
	if _, err := Write(dir, e1); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, e2); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
}

func TestList_NoDirReturnsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

func TestList_SkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "999.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := Entry{PID: 100, Address: "127.0.0.1:100"}
	if _, err := Write(dir, good); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].PID != 100 {
		t.Fatalf("expected [pid=100], got %#v", got)
	}
}

func TestDefaultDir_RespectsHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := DefaultDir()
	want := "/tmp/fakehome/.serf/run"
	if got != want {
		t.Fatalf("DefaultDir: got %q, want %q", got, want)
	}
}
