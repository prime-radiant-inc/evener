//go:build unix

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

func TestStableDelegateReadOnly_TornTailIsReportedButNotRepaired(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := seedStableReadonlyTornJournal(t, stateDir, sessionID)
	want := mustReadonlyFileState(t, path)

	tree, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree torn tail: %v", err)
	}
	diagnostics := stableReadonlyRootDiagnostics(t, tree)
	if !strings.Contains(strings.Join(diagnostics, "\n"), "delegate_journal_torn_tail") {
		t.Fatalf("torn-tail diagnostics = %#v", diagnostics)
	}
	if got := mustReadonlyFileState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("torn-tail read repaired or mutated journal:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestStableDelegateReadOnly_FileBytesAndMetadataRemainUnchanged(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := seedStableReadonlyTornJournal(t, stateDir, sessionID)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(12345, 0).UTC()
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	want := mustReadonlyFileState(t, path)

	if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	if got := mustReadonlyFileState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only projection changed bytes or metadata:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestStableDelegateReadOnly_NoSessionProviderOrWritableOpen(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Unix(100, 0).UTC()
	if err := store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, JobID: "job_readonly", Type: jobstore.JobShell,
		OwnerSessionID: sessionID, StartedAt: &startedAt,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(12345, 0).UTC()
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	want := mustReadonlyFileState(t, path)

	if _, err := LoadSessionHistoricalJobRecords(stateDir, sessionID); err != nil {
		t.Fatalf("historical record projection constructed writable state: %v", err)
	}
	if _, err := LoadSessionJobActivityTree(context.Background(), stateDir, sessionID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("historical activity projection constructed writable state: %v", err)
	}
	if _, found, err := LoadSessionJobOutputTail(stateDir, sessionID, "job_readonly", 0, 1); err != nil || !found {
		t.Fatalf("historical output-tail projection: found=%v err=%v", found, err)
	}
	if got := mustReadonlyFileState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("historical reads changed journal bytes or metadata:\n got=%#v\nwant=%#v", got, want)
	}
}

type stableReadonlyFileState struct {
	Inode uint64
	Size  int64
	Mode  os.FileMode
	Mtime time.Time
	Bytes []byte
	Hash  [sha256.Size]byte
}

func mustReadonlyFileState(t *testing.T, path string) stableReadonlyFileState {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("file info for %s has %T system data", path, info.Sys())
	}
	return stableReadonlyFileState{
		Inode: stat.Ino,
		Size:  info.Size(),
		Mode:  info.Mode(),
		Mtime: info.ModTime(),
		Bytes: bytes.Clone(raw),
		Hash:  sha256.Sum256(raw),
	}
}
