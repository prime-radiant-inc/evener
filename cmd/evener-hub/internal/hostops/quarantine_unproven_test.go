package hostops

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// truncatedStoreJSON is a corrupt store file that cannot yield a custody
// snapshot: it ends inside its record list.
const truncatedStoreJSON = `{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[`

func requireUnprovenReplacement(t *testing.T, store *Store, path string, original []byte) {
	t.Helper()
	signal := store.Quarantine()
	if signal == nil || !signal.Unproven || signal.QuarantinedFile != path {
		t.Fatalf("Quarantine() = %+v, want an unproven signal naming %s", signal, path)
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("the replacement store serves %d records, want it empty", len(records))
	}
	_, aside := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
	if !bytes.Equal(aside, original) {
		t.Fatalf("the aside file does not hold the corrupt bytes verbatim: %q", aside)
	}
	// Host changes resume: the replacement takes a write.
	if _, err := store.Create(NewRecord{ClientOperationID: "after", Host: "h1", Kind: KindRestart, Generation: 1, IncarnationID: "inc"}); err != nil {
		t.Fatalf("the replacement store refuses a write: %v", err)
	}
}

// A corrupt file whose custody snapshot is incomplete is set aside and an empty
// replacement serves, with a signal that says the custody was never proven.
func TestCorruptStoreWithIncompleteCustodyIsSetAsideAndHostChangesResume(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, truncatedStoreJSON)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	requireUnprovenReplacement(t, store, path, []byte(truncatedStoreJSON))
	// The truncated file still shows its declared mark, which the replacement
	// allocates above.
	if records := store.Records(); len(records) != 1 {
		t.Fatalf("records = %+v", records)
	} else if id, err := parseAllocatorID(records[0].ID); err != nil || id <= 2 {
		t.Fatalf("the replacement allocated id %q, want one above the truncated file's mark of 2", records[0].ID)
	}

	// The next boot reopens the replacement and still reports the unproven set-aside.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if signal := again.Quarantine(); signal == nil || !signal.Unproven {
		t.Fatalf("reopened Quarantine() = %+v, want the unproven signal to persist", signal)
	}
	if records := again.Records(); len(records) != 1 {
		t.Fatalf("the reopened store serves %d records, want the one written after the set-aside", len(records))
	}
}

// A crash between the rename and the replacement open is completed on the next
// boot, exactly as for a proven quarantine.
func TestUnprovenSetAsideCrashAfterRenameCompletesOnNextBoot(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, truncatedStoreJSON)

	_, err := openFS(afero.NewOsFs(), path, storeFaults{afterRename: func() error { return errors.New("crash after rename") }})
	if err == nil {
		t.Fatal("the crashing boot served")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the crash left a store at the path (stat err = %v)", statErr)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("recovery boot: %v", err)
	}
	requireUnprovenReplacement(t, store, path, []byte(truncatedStoreJSON))
}

// A non-regular store path is not set aside: the rename would refuse it.
func TestNonRegularCorruptStorePathIsNotSetAside(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	target := filepath.Join(dir, "elsewhere")
	writeRawStore(t, target, 0o600, truncatedStoreJSON)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open set a symlinked store aside")
	}
	if body := mustReadFile(t, target); string(body) != truncatedStoreJSON {
		t.Fatalf("the symlink target was disturbed: %q", body)
	}
}

// The set-aside keeps the allocator floor the corrupt file still shows, so an
// operation id the old file handed out is never handed out again.
func TestUnprovenSetAsideKeepsTheAllocatorFloor(t *testing.T) {
	path := StorePath(t.TempDir())
	body := `{"version":1,"sequence":1,"allocatorHighWaterMark":40,"records":[` + absurdHWMTerminalRecordJSON() + `]}`
	writeRawStore(t, path, 0o600, body)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	created, err := store.Create(NewRecord{ClientOperationID: "after", Host: "h1", Kind: KindRestart, Generation: 1, IncarnationID: "inc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id, err := parseAllocatorID(created.ID); err != nil || id <= 40 {
		t.Fatalf("the replacement allocated id %q, want one above the set-aside file's high-water mark", created.ID)
	}
}

// Record and tombstone ids the corrupt file still shows raise the floor even
// when its declared high-water mark sits below them.
func TestUnprovenSetAsideKeepsTheFloorOfVisibleRecordIDs(t *testing.T) {
	path := StorePath(t.TempDir())
	body := `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[` + absurdHWMTerminalRecordJSON() + `]}`
	writeRawStore(t, path, 0o600, body)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	created, err := store.Create(NewRecord{ClientOperationID: "after", Host: "h1", Kind: KindRestart, Generation: 1, IncarnationID: "inc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id, err := parseAllocatorID(created.ID); err != nil || id <= 1 {
		t.Fatalf("the replacement allocated id %q, want one above the id the set-aside file shows", created.ID)
	}
}

// A malformed region elsewhere in the file does not discard the allocator
// evidence the other regions still show.
func TestUnprovenSetAsideKeepsTheFloorBesideAMalformedCollection(t *testing.T) {
	path := StorePath(t.TempDir())
	body := `{"version":1,"sequence":0,"allocatorHighWaterMark":40,"records":[],"tombstones":{}}`
	writeRawStore(t, path, 0o600, body)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	created, err := store.Create(NewRecord{ClientOperationID: "after", Host: "h1", Kind: KindRestart, Generation: 1, IncarnationID: "inc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id, err := parseAllocatorID(created.ID); err != nil || id <= 40 {
		t.Fatalf("the replacement allocated id %q, want one above the file's high-water mark", created.ID)
	}
}
