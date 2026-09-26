package hostops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

// validStoreJSON is a minimal store file in the on-disk shape: the file
// version, the durable state-transition sequence, the controller-assigned row
// id allocator's high-water mark, and the record list. Tests that need a
// hand-written store file start from this literal rather than from the
// implementation's marshaler, so the on-disk schema is pinned by the test and
// not by the code under test.
const validStoreJSON = `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`

// openTestStore opens a store under a fresh temp state root.
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := StorePath(t.TempDir())
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return store, path
}

// createTestRecord persists one pending deploy record for host.
func createTestRecord(t *testing.T, store *Store, host string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{
		ClientOperationID: "client-" + host,
		Host:              host,
		Kind:              KindDeploy,
		Generation:        7,
		IncarnationID:     "incarnation-" + host,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", host, err)
	}
	return record
}

// leftoverTemps lists the store's temp files still present in dir. The atomic
// write creates them beside the store and must never leave one behind.
func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var temps []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			temps = append(temps, entry.Name())
		}
	}
	return temps
}

// mustReadFile returns the store file's bytes.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return raw
}

func TestStorePathSitsBesideTheOtherDurableStores(t *testing.T) {
	root := string(os.PathSeparator) + "state"
	if got, want := StorePath(root), filepath.Join(root, "hostops", "operations.json"); got != want {
		t.Fatalf("StorePath(/state) = %q, want %q", got, want)
	}
}

func TestOpenMissingStoreStartsEmptyAndWritesNothing(t *testing.T) {
	store, path := openTestStore(t)
	if got := store.Sequence(); got != 0 {
		t.Fatalf("a fresh store's sequence = %d, want 0", got)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("a fresh store holds %d records, want none", len(got))
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening a missing store created %s (stat err = %v)", path, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening a missing store created its directory %s (stat err = %v)", filepath.Dir(path), err)
	}
}

func TestCreateWritesOwnerOnlyStoreAtomically(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("store mode = %04o, want 0600", got)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("the atomic write left temp files behind: %v", temps)
	}

	var onDisk struct {
		Version uint64   `json:"version"`
		Records []Record `json:"records"`
	}
	raw := mustReadFile(t, path)
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("store file is not valid JSON: %v\n%s", err, raw)
	}
	if len(onDisk.Records) != 1 || onDisk.Records[0].ID != record.ID {
		t.Fatalf("store file records = %+v, want the one created record %q", onDisk.Records, record.ID)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reloaded, ok := reopened.Record(record.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", record.ID)
	}
	if reloaded.ClientOperationID != record.ClientOperationID || reloaded.State != StatePending {
		t.Fatalf("reloaded record = %+v, want the created pending record %+v", reloaded, record)
	}
}

// TestReplacementPreservesTheStoresOwnerMode pins the durability paragraph's
// "The store file and its temp files carry mode 0600. Replacements preserve
// the mode": a stricter owner-only mode that the file already carried is not
// widened by the next atomic replacement.
func TestReplacementPreservesTheStoresOwnerMode(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("Chmod(%s, 0400): %v", path, err)
	}
	if _, err := store.Transition(record.ID, StateComplete, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("store mode = %04o after the replacement, want the pre-existing 0400 preserved", got)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("the replacement left temp files behind: %v", temps)
	}
}

func TestOpenRefusesAStoreReadableBeyondItsOwner(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o660, 0o604} {
		t.Run(mode.String(), func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, mode, validStoreJSON)
			if _, err := Open(path); !errors.Is(err, ErrStoreReadableBeyondOwner) {
				t.Fatalf("Open on a %04o store: err = %v, want ErrStoreReadableBeyondOwner", mode, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat(%s): %v", path, err)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Fatalf("the refused load rewrote the store's mode to %04o, want %04o untouched", got, mode)
			}
		})
	}
}

// TestOpenRefusesACorruptStore pins this layer's half of spec §4's corrupt-store
// rule: a corrupt or schema-invalid store file is refused (startup does not
// serve it). The custody-first quarantine that §4 also takes at boot belongs to
// the crash-fencing slice; this layer refuses and nothing more.
func TestOpenRefusesACorruptStore(t *testing.T) {
	record := `{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1",` +
		`"kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1",` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}`
	cases := map[string]string{
		"truncated json":             `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[`,
		"trailing json value":        validStoreJSON + `{"version":1}`,
		"unknown field":              `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"compactSeq":0}`,
		"unsupported version":        `{"version":2,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		"duplicate record id":        `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + record + `,` + record + `]}`,
		"invalid state":              `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"state":"pending"`, `"state":"queued"`, 1) + `]}`,
		"record sequence ahead":      `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"sequence":9`, 1) + `]}`,
		"record without pinned pair": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"generation":7`, `"generation":0`, 1) + `]}`,
		"orphan state without boundary": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"state":"pending"`, `"state":"orphan-unverified"`, 1) + `]}`,
		"empty file": ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Open on a corrupt store: err = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestAFailedWriteIsNotAWrite pins the other half of the atomic write: a write
// that never lands leaves no temp file and no store file behind, and the store's
// in-memory state does not lead the file — memory that ran ahead of the durable
// store would answer a later read with a record nothing has recorded.
func TestAFailedWriteIsNotAWrite(t *testing.T) {
	path := StorePath(t.TempDir())
	failing := true
	store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
		if failing {
			return errors.New("before-rename fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}

	if _, err := store.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	}); err == nil {
		t.Fatalf("Create whose rename never landed reported success")
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("a write that never landed left %d records in memory, want none", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write that never landed left the store file behind (stat err = %v)", err)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("a write that never landed left temp files behind: %v", temps)
	}

	failing = false
	record, err := store.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	})
	if err != nil {
		t.Fatalf("Create once the fault cleared: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("store mode = %04o after the retry, want 0600", got)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := reopened.Record(record.ID); !ok {
		t.Fatalf("the record the retry created %q did not survive the reload", record.ID)
	}
}

// TestOpenFailsOnAStoreItCannotRead pins this layer's answer to a store it
// cannot read at all: refusal, never a half-served store. The custody-first
// quarantine spec §4 takes for a corrupt store is the crash-fencing slice's.
func TestOpenFailsOnAStoreItCannotRead(t *testing.T) {
	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses filesystem permission checks")
		}
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, validStoreJSON)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("Chmod(%s, 0000): %v", path, err)
		}
		if _, err := Open(path); err == nil {
			t.Fatalf("Open on an unreadable store succeeded, want an error")
		}
	})
	t.Run("store path is a directory", func(t *testing.T) {
		dir := StorePath(t.TempDir())
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatalf("Open on a directory succeeded, want an error")
		}
	})
	t.Run("store path under a regular file", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", blocker, err)
		}
		if _, err := Open(filepath.Join(blocker, "operations.json")); err == nil {
			t.Fatalf("Open under a regular file succeeded, want an error")
		}
	})
}

func TestStoreSerializesConcurrentReadModifyWrite(t *testing.T) {
	store, path := openTestStore(t)
	const writers = 8

	var wg sync.WaitGroup
	errs := make([]error, writers)
	ids := make([]string, writers)
	for i := range writers {
		wg.Go(func() {
			record, err := store.Create(NewRecord{
				ClientOperationID: "concurrent",
				Host:              "h1",
				Kind:              KindRestart,
				Generation:        7,
				IncarnationID:     "inc-1",
			})
			errs[i], ids[i] = err, record.ID
		})
	}
	wg.Wait()

	seen := make(map[string]bool, writers)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create %d: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("concurrent Create handed out id %q twice", ids[i])
		}
		seen[ids[i]] = true
	}
	if got := len(store.Records()); got != writers {
		t.Fatalf("store holds %d records after %d concurrent creates, want %d", got, writers, writers)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.Records()); got != writers {
		t.Fatalf("store file holds %d records after %d concurrent creates, want %d", got, writers, writers)
	}
}

// TestCreateRefusesRecordsOutsideTheStoreSchema pins the schema validation this
// layer applies to the fields spec §1 gives shapes for.
func TestCreateRefusesRecordsOutsideTheStoreSchema(t *testing.T) {
	cases := map[string]NewRecord{
		"empty client operation id": {ClientOperationID: "", Host: "h1", Kind: KindDeploy, Generation: 1, IncarnationID: "inc"},
		"client operation id over 128 bytes": {
			ClientOperationID: strings.Repeat("x", 129), Host: "h1", Kind: KindDeploy, Generation: 1, IncarnationID: "inc",
		},
		"empty host":        {ClientOperationID: "op", Host: "", Kind: KindDeploy, Generation: 1, IncarnationID: "inc"},
		"unknown kind":      {ClientOperationID: "op", Host: "h1", Kind: Kind("migrate"), Generation: 1, IncarnationID: "inc"},
		"zero generation":   {ClientOperationID: "op", Host: "h1", Kind: KindDeploy, Generation: 0, IncarnationID: "inc"},
		"empty incarnation": {ClientOperationID: "op", Host: "h1", Kind: KindDeploy, Generation: 1, IncarnationID: ""},
	}
	for name, new := range cases {
		t.Run(name, func(t *testing.T) {
			store, path := openTestStore(t)
			if _, err := store.Create(new); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Create(%+v): err = %v, want ErrInvalidRecord", new, err)
			}
			if got := len(store.Records()); got != 0 {
				t.Fatalf("a refused Create left %d records in the store", got)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused Create wrote the store file (stat err = %v)", err)
			}
		})
	}
}

// writeRawStore writes body as the store file with exactly mode: os.WriteFile
// applies the process umask, so the mode is forced afterwards.
func writeRawStore(t *testing.T, path string, mode os.FileMode, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(%s, %04o): %v", path, mode, err)
	}
}
