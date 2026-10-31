package hostops

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func createdID(t *testing.T, store *Store) uint64 {
	t.Helper()
	created, err := store.Create(NewRecord{ClientOperationID: "after", Host: "h1", Kind: KindRestart, Generation: 1, IncarnationID: "inc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, err := parseAllocatorID(created.ID)
	if err != nil {
		t.Fatalf("created id %q: %v", created.ID, err)
	}
	return id
}

// The replacement's ids start above every id an earlier store could have
// issued without the corrupt bytes being read: garbage, a declared mark and a
// repeated member all get the same floor, and a second set-aside starts above
// the ids the first replacement issued.
func TestUnprovenSetAsideFloorIsIndependentOfTheCorruptBytes(t *testing.T) {
	bodies := map[string]string{
		"binary garbage":    "\x00\xff\xfe not json at all \x01",
		"a declared mark":   `{"version":1,"sequence":0,"allocatorHighWaterMark":40,"records":[` + absurdHWMTerminalRecordJSON() + `]}`,
		"a repeated member": `{"version":1,"allocatorHighWaterMark":40,"allocatorHighWaterMark":0,"records":[]}`,
		"a truncated file":  truncatedStoreJSON,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			first, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			firstID := createdID(t, first)
			if firstID <= allocatorEpochStride {
				t.Fatalf("first replacement issued id %d, want one above the epoch stride", firstID)
			}
			// Corrupt the replacement as well, after it has issued ids.
			if err := forgetStore(path); err != nil {
				t.Fatal(err)
			}
			writeRawStore(t, path, 0o600, body)
			second, err := Open(path)
			if err != nil {
				t.Fatalf("second Open: %v", err)
			}
			if id := createdID(t, second); id <= firstID {
				t.Fatalf("second replacement issued id %d, want one above the first replacement's %d", id, firstID)
			}
		})
	}
}

// A store opened over an unproven set-aside skips a wide id range, and records
// that in its own file. Corrupting only a region the quarantine discards, later,
// must not make that range look like lost records, even after the quarantine
// artifacts are gone: the host records survive a proven quarantine.
func TestSkippedIDRangeDoesNotMakeALaterQuarantineUnproven(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, truncatedStoreJSON)
	unproven, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := unproven.ImportHostRecords(familyOf("first"), HostMirror{}); err != nil {
		t.Fatalf("ImportHostRecords: %v", err)
	}
	for _, infix := range []string{".custody-", ".quarantined-"} {
		artifact, _ := quarantineArtifact(t, filepath.Dir(path), infix)
		if err := os.Remove(artifact); err != nil {
			t.Fatalf("remove %s: %v", artifact, err)
		}
	}
	raw := string(mustReadFile(t, path))
	broken := strings.Replace(raw, `"tokens":[]`, `"tokens":7`, 1)
	if broken == raw {
		t.Fatalf("the fixture carries no tokens region:\n%s", raw)
	}
	writeRawStore(t, path, 0o600, broken)
	if err := forgetStore(path); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open over the corrupted replacement: %v", err)
	}
	if signal := reopened.Quarantine(); signal == nil || signal.Unproven || signal.QuarantineEpoch != 2 {
		t.Fatalf("Quarantine() = %+v, want a proven quarantine at epoch 2", signal)
	}
	if id := createdID(t, reopened); id <= allocatorEpochStride {
		t.Fatalf("the lineage's floor was lost: issued id %d", id)
	}
	if family, _, _ := reopened.HostRecords(); len(family.Hosts) == 0 {
		t.Fatal("the host records were dropped")
	}
}
