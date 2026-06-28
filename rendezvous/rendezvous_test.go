package rendezvous

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

func TestEntryRoundTripIncludesAppWireEndpoint(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{
		PID:       123,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:49152/rpc",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
		HubToken:  "secret-token",
	}
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}
	if entries[0].Protocol != entry.Protocol ||
		entries[0].Endpoint != entry.Endpoint ||
		entries[0].SourceID != entry.SourceID ||
		entries[0].SessionID != entry.SessionID ||
		entries[0].ThreadID != entry.ThreadID {
		t.Fatalf("AppWire fields mismatch: got %+v, want %+v", entries[0], entry)
	}
	if entries[0].HubToken != entry.HubToken {
		t.Fatalf("hub token was not preserved")
	}
}

func TestWrite_UsesPrivatePermissionsForTokenFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "rdv")
	// pre-create with loose permissions so Write must fix them via Chmod
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Write(dir, Entry{PID: 12345, Address: "127.0.0.1:1", HubToken: "secret-token"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode=%#o, want 0600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode=%#o, want 0700", got)
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
	sort.Slice(got, func(i, j int) bool { return got[i].PID < got[j].PID })
	if got[0].PID != e1.PID || got[0].Address != e1.Address {
		t.Fatalf("entry[0] mismatch: got %#v, want %#v", got[0], e1)
	}
	if got[1].PID != e2.PID || got[1].Address != e2.Address {
		t.Fatalf("entry[1] mismatch: got %#v, want %#v", got[1], e2)
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

func TestWrite_MkdirAllFails(t *testing.T) {
	// Create a file at the path where MkdirAll would need to create a directory.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "isfile")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pass filePath as the dir argument; MkdirAll will fail because it exists and is not a dir.
	_, err := Write(filePath, Entry{PID: 1, Address: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
	if !strings.Contains(err.Error(), "create rendezvous dir") {
		t.Fatalf("expected error from the MkdirAll branch, got %v", err)
	}
}

func TestWrite_NestedDirParentIsFile(t *testing.T) {
	// Make a parent path component a regular file so MkdirAll returns ENOTDIR.
	// This is root-proof: the OS rejects creating a dir under a file even for
	// uid 0, unlike chmod-based permission tricks that root bypasses.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Write(filepath.Join(blocker, "child"), Entry{PID: 1, Address: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected error when MkdirAll fails because a parent component is a file")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("expected ENOTDIR, got %v", err)
	}
}

func TestWrite_TmpPathIsDirectory(t *testing.T) {
	// Pre-create the temporary file path as a directory so WriteFile fails.
	dir := t.TempDir()
	entry := Entry{PID: 1, Address: "127.0.0.1:1"}
	tmp := filepath.Join(dir, "1.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Write(dir, entry)
	if err == nil {
		t.Fatal("expected error when tmp path is a directory")
	}
	if !strings.Contains(err.Error(), "write tmp") {
		t.Fatalf("expected error from the WriteFile branch, got %v", err)
	}
}

func TestWrite_TargetIsDirectory(t *testing.T) {
	// Pre-create the target file path as a directory so Rename fails.
	dir := t.TempDir()
	entry := Entry{PID: 1, Address: "127.0.0.1:1"}
	target := filepath.Join(dir, "1.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Write(dir, entry)
	if err == nil {
		t.Fatal("expected error when target path is a directory")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Fatalf("expected error from the Rename branch, got %v", err)
	}
}
