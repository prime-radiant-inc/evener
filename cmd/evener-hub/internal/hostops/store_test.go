package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

// reopenFresh opens a store at path the way a process restart does: it forgets
// the path's shared cell first, so what the test reads next comes from the file
// rather than from a live handle's in-memory state.
func reopenFresh(t *testing.T, path string) *Store {
	t.Helper()
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore(%s): %v", path, err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	return store
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

	reopened := reopenFresh(t, path)
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
		// A file that omits a top-level field is not a store the writer ever
		// produced, and reading it as an empty store would let the next write
		// replace real history with nothing.
		"missing sequence":                  `{"version":1,"allocatorHighWaterMark":0,"records":[]}`,
		"missing allocator high-water mark": `{"version":1,"sequence":0,"records":[]}`,
		"missing record list":               `{"version":1,"sequence":0,"allocatorHighWaterMark":0}`,
		"null record list":                  `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":null}`,
		"null sequence":                     `{"version":1,"sequence":null,"allocatorHighWaterMark":0,"records":[]}`,
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
	reopened := reopenFresh(t, path)
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
	reopened := reopenFresh(t, path)
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

// recordJSON renders one pending record in the store file's shape with the given
// controller-assigned id, for the fixtures that pin the id rules.
func recordJSON(id, state string) string {
	return `{"id":"` + id + `","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"` + state + `",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z",` +
		`"updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}`
}

// TestOpenAcceptsALegitimatelyEmptyStore pins the other side of the presence
// rule: a store with no records yet is a normal store, not a corrupt one.
func TestOpenAcceptsALegitimatelyEmptyStore(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, validStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("a legitimately empty store was refused: %v", err)
	}
	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d, want 0", got)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("records = %d, want none", len(got))
	}
}

// TestOpenRefusesIdsOutsideTheAllocatorForm pins the id form the store's own
// allocator emits, which spec §8's ordering depends on: the controller-assigned
// id is a fixed-width decimal string, and a record carrying any other width —
// or a record set stored out of id order — is not a store this writer produced.
// Accepting one would let string order (what §8 sorts and resumes by) disagree
// with allocation order.
func TestOpenRefusesIdsOutsideTheAllocatorForm(t *testing.T) {
	cases := map[string]string{
		"plain decimal id": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			recordJSON("1", "pending") + `]}`,
		"over-wide id": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			recordJSON("000000000000000000001", "pending") + `]}`,
		"reverse id order": `{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[` +
			recordJSON("00000000000000000002", "pending") + `,` + recordJSON("00000000000000000001", "pending") + `]}`,
		"duplicate id order": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			recordJSON("00000000000000000001", "pending") + `,` + recordJSON("00000000000000000001", "pending") + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Open on %s: err = %v, want ErrStoreCorrupt", name, err)
			}
		})
	}
}

// TestAllocatorIdsAreCanonicalAndAscending is the positive control for the id
// rules: what the allocator emits is exactly the form the loader accepts, and
// what it stores is ascending, across a reload.
func TestAllocatorIdsAreCanonicalAndAscending(t *testing.T) {
	store, path := openTestStore(t)
	first := createTestRecord(t, store, "h1")
	second := createTestRecord(t, store, "h2")
	for _, record := range []Record{first, second} {
		parsed, err := parseAllocatorID(record.ID)
		if err != nil {
			t.Fatalf("the allocator emitted id %q the loader refuses: %v", record.ID, err)
		}
		if record.ID != formatAllocatorID(parsed) {
			t.Fatalf("id %q is not in the allocator's canonical form", record.ID)
		}
	}
	reloaded := reopenFresh(t, path)
	records := reloaded.Records()
	if len(records) != 2 {
		t.Fatalf("reloaded %d records, want 2", len(records))
	}
	if records[0].ID >= records[1].ID {
		t.Fatalf("stored order is not ascending: %q then %q", records[0].ID, records[1].ID)
	}
}

// readStoreSnapshot reads the store file the way a fresh process would, so a
// test can hold the durable state beside a handle's in-memory state.
func readStoreSnapshot(t *testing.T, path string) snapshot {
	t.Helper()
	var state snapshot
	raw := mustReadFile(t, path)
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("store file is not valid JSON: %v\n%s", err, raw)
	}
	return state
}

// TestAPostRenameFailureKeepsMemoryInStepWithTheFile pins spec §4's durability
// paragraph where the rename is the commit point: a failure behind it (here the
// directory sync that makes the rename durable) is not a refusal that wrote
// nothing. The store must adopt the state the file already holds — otherwise its
// next write would rewrite the whole file from a stale snapshot and silently
// revert the transition the rename committed, un-advancing the sequence with it.
func TestAPostRenameFailureKeepsMemoryInStepWithTheFile(t *testing.T) {
	path := StorePath(t.TempDir())
	var syncErr error
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(afero.Fs, string) error {
		return syncErr
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	record := createTestRecord(t, store, "h1")
	running, err := store.Transition(record.ID, StateRunning, nil)
	if err != nil {
		t.Fatalf("Transition(running): %v", err)
	}

	// Only this write fails behind its rename.
	syncErr = errors.New("directory sync fault")
	landed, err := store.Transition(running.ID, StateComplete, nil)
	if err == nil {
		t.Fatalf("a write whose directory sync failed reported success")
	}
	if !RenameLanded(err) {
		t.Fatalf("the post-rename failure is not distinguishable: %v", err)
	}
	if landed.State != StateComplete || landed.Sequence != 1 {
		t.Fatalf("the landed transition returned %+v, want the committed complete record", landed)
	}

	// (a) The transition the rename committed is the durable one.
	onDisk := readStoreSnapshot(t, path)
	if onDisk.Sequence != 1 {
		t.Fatalf("on-disk sequence = %d, want the committed 1", onDisk.Sequence)
	}
	// (b) Memory and the file hold the same thing.
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateComplete || stored.Sequence != onDisk.Sequence {
		t.Fatalf("in-memory record = %+v, file holds sequence %d: memory is behind the file", stored, onDisk.Sequence)
	}
	if got := store.Sequence(); got != onDisk.Sequence {
		t.Fatalf("in-memory sequence = %d, file = %d: memory is behind the file", got, onDisk.Sequence)
	}
	if got, want := marshalRecords(t, store.Records()), marshalRecords(t, onDisk.Records); !bytes.Equal(got, want) {
		t.Fatalf("in-memory records differ from the file:\nmemory: %s\nfile:   %s", got, want)
	}

	// (c) The next write does not revert the committed transition.
	syncErr = nil
	second := createTestRecord(t, store, "h2")
	stored, ok = store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateComplete || stored.Sequence != onDisk.Sequence {
		t.Fatalf("the next write reverted the committed transition: %+v", stored)
	}
	reloaded := reopenFresh(t, path)
	again, ok := reloaded.Record(record.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", record.ID)
	}
	if again.State != StateComplete || again.Sequence != onDisk.Sequence {
		t.Fatalf("the committed transition was lost from the file: %+v", again)
	}
	if got := len(reloaded.Records()); got != 2 {
		t.Fatalf("the file holds %d records, want the committed transition plus %q", got, second.ID)
	}
}

// TestTwoHandlesForOneStoreShareTheOneStoreMutex pins spec §4's "one store-wide
// mutex": every read-modify-write of a store holds it. Two handles for one path
// must therefore serialize on the same lock over the same state; two independent
// locks over two stale snapshots would let each handle rewrite the whole file
// from its own view and drop the other's records.
func TestTwoHandlesForOneStoreShareTheOneStoreMutex(t *testing.T) {
	path := StorePath(t.TempDir())
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}

	if _, err := first.Create(NewRecord{ClientOperationID: "via-first", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1"}); err != nil {
		t.Fatalf("Create through the first handle: %v", err)
	}
	if _, err := second.Create(NewRecord{ClientOperationID: "via-second", Host: "h1", Kind: KindRestart, Generation: 7, IncarnationID: "inc-1"}); err != nil {
		t.Fatalf("Create through the second handle: %v", err)
	}
	if got := len(first.Records()); got != 2 {
		t.Fatalf("the first handle sees %d records, want 2: the handles do not share one store", got)
	}
	if got := len(second.Records()); got != 2 {
		t.Fatalf("the second handle sees %d records, want 2", got)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		handle := first
		if i%2 == 1 {
			handle = second
		}
		wg.Go(func() {
			_, errs[i] = handle.Create(NewRecord{ClientOperationID: "concurrent", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1"})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create %d: %v", i, err)
		}
	}
	records := first.Records()
	if len(records) != writers+2 {
		t.Fatalf("the two handles hold %d records, want %d: a record was lost", len(records), writers+2)
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.ID] {
			t.Fatalf("id %q was handed out twice", record.ID)
		}
		seen[record.ID] = true
	}
	reloaded := reopenFresh(t, path)
	if got := len(reloaded.Records()); got != writers+2 {
		t.Fatalf("the store file holds %d records, want %d", got, writers+2)
	}
}

// TestANilStoreAnswersSafely pins the nil-handle guards: every method answers
// without dereferencing the handle, so a caller that wires the store up lazily
// cannot panic the hub.
func TestANilStoreAnswersSafely(t *testing.T) {
	var store *Store
	if got := store.Path(); got != "" {
		t.Fatalf("Path() = %q, want empty", got)
	}
	if got := store.Sequence(); got != 0 {
		t.Fatalf("Sequence() = %d, want 0", got)
	}
	if got := store.Records(); got != nil {
		t.Fatalf("Records() = %v, want nil", got)
	}
	if _, ok := store.Record("00000000000000000001"); ok {
		t.Fatalf("Record on a nil store found a record")
	}
	if _, err := store.Create(NewRecord{ClientOperationID: "op", Host: "h1", Kind: KindDeploy, Generation: 1, IncarnationID: "inc"}); err == nil {
		t.Fatalf("Create on a nil store succeeded")
	}
	if _, err := store.Transition("00000000000000000001", StateComplete, nil); err == nil {
		t.Fatalf("Transition on a nil store succeeded")
	}
	if _, err := store.RecoverInterrupted(); err == nil {
		t.Fatalf("RecoverInterrupted on a nil store succeeded")
	}
}

// terminalRecordJSON renders one terminal record in the store file's shape,
// stamped with the given sequence value.
func terminalRecordJSON(id string, stamp uint64) string {
	return `{"id":"` + id + `","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z",` +
		`"updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"sequence":` + strconv.FormatUint(stamp, 10) + `}`
}

// TestOpenRefusesDuplicateStampsButAcceptsGaps pins the sequence stamp's
// uniqueness: every terminal transition advances the durable sequence once and
// stamps the record it moved, so one stamp belongs to exactly one record — while
// the gaps retention and compaction will leave are legitimate.
func TestOpenRefusesDuplicateStampsButAcceptsGaps(t *testing.T) {
	duplicate := `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
		terminalRecordJSON("00000000000000000001", 1) + `,` + terminalRecordJSON("00000000000000000002", 1) + `]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, duplicate)
	if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Open on a store with two records sharing a stamp: err = %v, want ErrStoreCorrupt", err)
	}

	gappy := `{"version":1,"sequence":9,"allocatorHighWaterMark":2,"records":[` +
		terminalRecordJSON("00000000000000000001", 4) + `,` + terminalRecordJSON("00000000000000000002", 9) + `]}`
	gapPath := StorePath(t.TempDir())
	writeRawStore(t, gapPath, 0o600, gappy)
	store, err := Open(gapPath)
	if err != nil {
		t.Fatalf("a store with sequence gaps was refused: %v", err)
	}
	if got := store.Sequence(); got != 9 {
		t.Fatalf("sequence = %d, want the persisted 9", got)
	}
}

// TestOpenRefusesBoundariesAndEpochsOutsideTheirJSONShapes pins the fail-closed
// presence rules for the two raw fields. An absent field is absent; a present
// one must carry the shape the spec gives it — a member array for an
// orphan-unverified record's boundary, an object for a fencing epoch — because a
// boundary that reads as null, empty or a scalar is what the fencing path would
// unmarshal into an empty list, and "demonstrably empty" is the reading that
// clears a fence.
func TestOpenRefusesBoundariesAndEpochsOutsideTheirJSONShapes(t *testing.T) {
	orphan := func(boundary string) string {
		return `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"orphan-unverified","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"orphanBoundary":` + boundary + `}]}`
	}
	pending := func(epoch string) string {
		return `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"fencingEpoch":` + epoch + `}]}`
	}
	refused := map[string]string{
		"null boundary":     orphan("null"),
		"empty array":       orphan("[]"),
		"object boundary":   orphan("{}"),
		"scalar boundary":   orphan("123"),
		"string boundary":   orphan(`"x"`),
		"unparseable array": orphan("["),
		"null epoch":        pending("null"),
		"array epoch":       pending("[]"),
		"scalar epoch":      pending("7"),
	}
	for name, body := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Open on a %s: err = %v, want ErrStoreCorrupt", name, err)
			}
		})
	}
	accepted := map[string]string{
		"member array": orphan(`[{"host":"h1","kind":"local-linux"}]`),
		"object epoch": pending(`{"bootId":"boot-1","opSeq":3}`),
		"absent epoch": pendingPayloadWithoutEpoch(),
	}
	for name, body := range accepted {
		t.Run("accepted/"+name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); err != nil {
				t.Fatalf("Open on a %s was refused: %v", name, err)
			}
		})
	}
}

// pendingPayloadWithoutEpoch is a pending record carrying no fencing epoch at
// all, which is what the create write lands: the epoch arrives before the first
// running probe.
func pendingPayloadWithoutEpoch() string {
	return `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
}

// TestALandedWriteIsReconcilable pins the caller-visible half of a post-rename
// failure: the write landed, so the caller must be able to reconcile rather than
// treat the operation as absent and open a duplicate. Create returns the record
// it persisted, RenameLanded answers the classification, and the record is
// readable from the store and from a fresh load. A failure before the rename is
// the other case: no record, and RenameLanded says so.
func TestALandedWriteIsReconcilable(t *testing.T) {
	path := StorePath(t.TempDir())
	var syncErr error
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(afero.Fs, string) error {
		return syncErr
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}

	syncErr = errors.New("directory sync fault")
	record, err := store.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	})
	if err == nil {
		t.Fatalf("Create whose directory sync failed reported success")
	}
	if !RenameLanded(err) {
		t.Fatalf("Create's post-rename failure is not distinguishable: %v", err)
	}
	if record.ID == "" {
		t.Fatalf("a landed Create returned no record id to reconcile with")
	}
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("the reconciled id %q is not in the store", record.ID)
	}
	if stored.ClientOperationID != "client-h1" || stored.State != StatePending {
		t.Fatalf("reconciled record = %+v, want the pending record the write committed", stored)
	}

	syncErr = nil
	reloaded := reopenFresh(t, path)
	if _, ok := reloaded.Record(record.ID); !ok {
		t.Fatalf("the landed record %q is not in the file", record.ID)
	}

	// The pre-rename failure is not a landed write.
	blocked := StorePath(t.TempDir())
	refusing, err := openFS(afero.NewOsFs(), blocked, storeFaults{beforeRename: func() error {
		return errors.New("before-rename fault")
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	none, err := refusing.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	})
	if err == nil {
		t.Fatalf("Create with a failing rename reported success")
	}
	if RenameLanded(err) {
		t.Fatalf("a pre-rename refusal was reported as landed: %v", err)
	}
	if none.ID != "" {
		t.Fatalf("a refusal returned record %q, want none", none.ID)
	}
	if len(refusing.Records()) != 0 {
		t.Fatalf("a refused Create left records behind")
	}
}
