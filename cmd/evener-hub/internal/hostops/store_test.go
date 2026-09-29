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
	"time"

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
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
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

// TestOpenRefusesACorruptStore pins the refusal half of spec §4's corrupt-store
// rule: a corrupt or schema-invalid store file whose custody snapshot cannot be
// shown complete refuses the load (startup does not serve it), and the refused
// file is never rewritten. The quarantine half — a corrupt file that yields a
// complete custody snapshot — runs through the same Open path (quarantine.go /
// custody.go).
func TestOpenRefusesACorruptStore(t *testing.T) {
	record := `{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1",` +
		`"kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1",` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}`
	cases := map[string]string{
		"truncated json":      `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[`,
		"trailing json value": validStoreJSON + `{"version":1}`,
		// A key this store's writer never emits (S6 added compactSeq, so the
		// stand-in for "unknown" moved to a key no slice owns).
		"unknown field":              `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"unknownTopLevelKey":0}`,
		"unsupported version":        `{"version":2,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		"duplicate record id":        `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + record + `,` + record + `]}`,
		"invalid state":              `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"state":"pending"`, `"state":"queued"`, 1) + `]}`,
		"record sequence ahead":      `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"sequence":9`, 1) + `]}`,
		"record without pinned pair": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` + strings.Replace(record, `"generation":7`, `"generation":0`, 1) + `]}`,
		"empty file":                 ``,
		// A file that omits a top-level field is not a store the writer ever
		// produced, and reading it as an empty store would let the next write
		// replace real history with nothing.
		"missing sequence":                  `{"version":1,"allocatorHighWaterMark":0,"records":[]}`,
		"missing allocator high-water mark": `{"version":1,"sequence":0,"records":[]}`,
		"missing record list":               `{"version":1,"sequence":0,"allocatorHighWaterMark":0}`,
		"null record list":                  `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":null}`,
		"null sequence":                     `{"version":1,"sequence":null,"allocatorHighWaterMark":0,"records":[]}`,
		// The writer always emits a record's host-removed mark and both halves of
		// a present result, so a file omitting either is not one it produced.
		"record without a host-removed mark": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `,"hostRemoved":false`, ``, 1) + `]}`,
		"null host-removed mark": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":null`, 1) + `]}`,
		"result without an outcome": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"result":{"message":"failed"}`, 1) + `]}`,
		"null result outcome": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"result":{"ok":null,"message":"failed"}`, 1) + `]}`,
		"null result": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"result":null`, 1) + `]}`,
		// A result is terminal data, and the file's strings must be valid UTF-8:
		// encoding/json replaces invalid bytes with U+FFFD on the way out, so a
		// value the store accepted would come back changed.
		"result on a pending record": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"result":{"ok":true,"message":"deployed"}`, 1) + `]}`,
		"invalid utf-8 in a message": `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(terminalRecordJSON("00000000000000000001", 1), `"hostRemoved":false`, `"hostRemoved":false,"result":{"ok":false,"message":"`+"\xff\xfe"+`"}`, 1) + `]}`,
		"invalid utf-8 in a raw field": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"fencingEpoch":{"bootId":"`+"\xff\xfe"+`","opSeq":3}`, 1) + `]}`,
		// The decoder keeps the last occurrence of a duplicated key silently, so a
		// file naming one twice would be read as the reduced state and rewritten
		// that way.
		"duplicate top-level key": `{"version":1,"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		"duplicate records key": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[],"records":[` +
			recordJSON("00000000000000000001", "pending") + `]}`,
		"duplicate key inside a record": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"host":"h1"`, `"host":"h1","host":"h2"`, 1) + `]}`,
		"duplicate key inside a raw field": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"fencingEpoch":{"bootId":"a","bootId":"b","opSeq":3}`, 1) + `]}`,
		// The writer omits progress until there is something to carry, so a
		// present null is not a shape it produced.
		"null progress list": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"hostRemoved":false`, `"hostRemoved":false,"progress":null`, 1) + `]}`,
		// A \u escape for an unpaired surrogate is ASCII in the raw bytes, so the
		// UTF-8 check cannot see it, and the decoder replaces it with U+FFFD: the
		// value the file denotes would come back changed.
		"lone high surrogate": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"host":"h1"`, `"host":"a\ud800b"`, 1) + `]}`,
		"lone low surrogate": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"host":"h1"`, `"host":"a\udc00b"`, 1) + `]}`,
		// A lone surrogate escape right after a valid pair: the scanner must step
		// over the pair only, or it never looks at the escape that follows it.
		"lone surrogate after a pair": `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(record, `"host":"h1"`, `"host":"a\ud83d\ude00\udc00b"`, 1) + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Open on a corrupt store: err = %v, want ErrStoreCorrupt", err)
			}
			// A refused file is never served and never rewritten: no temp file,
			// and the bytes on disk are exactly what the caller wrote.
			if got := string(mustReadFile(t, path)); got != body {
				t.Fatalf("a refused load rewrote the corrupt store:\nbefore: %s\nafter:  %s", body, got)
			}
			if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
				t.Fatalf("a refused load left temp files behind: %v", temps)
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
// cannot read at all (a filesystem error, not a parse failure): refusal, never a
// half-served store. The custody-first quarantine §4 defines applies to a
// corrupt file whose snapshot is provable, not to one this layer cannot read.
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
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			return nil
		}
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
	landed, err := store.Transition(running.ID, StateComplete, terminalChange(true))
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
	if _, err := store.Transition("00000000000000000001", StateComplete, terminalChange(true)); err == nil {
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
		`"updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,` +
		`"result":{"ok":true,"message":"done"},"sequence":` + strconv.FormatUint(stamp, 10) + `}`
}

// TestOpenQuarantinesDuplicateStampsButAcceptsGaps pins the sequence stamp's
// uniqueness: every terminal transition advances the durable sequence once and
// stamps the record it moved, so one stamp belongs to exactly one record. A file
// violating that is schema-invalid and takes §4's custody-first quarantine —
// never served as its own state — while the gaps retention and compaction will
// leave are legitimate and load untouched.
func TestOpenQuarantinesDuplicateStampsButAcceptsGaps(t *testing.T) {
	duplicate := `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
		terminalRecordJSON("00000000000000000001", 1) + `,` + terminalRecordJSON("00000000000000000002", 1) + `]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, duplicate)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a store with two records sharing a stamp: %v", err)
	}
	wantQuarantined(t, store, path, duplicate)

	gappy := `{"version":1,"sequence":9,"allocatorHighWaterMark":2,"records":[` +
		terminalRecordJSON("00000000000000000001", 4) + `,` + terminalRecordJSON("00000000000000000002", 9) + `]}`
	gapPath := StorePath(t.TempDir())
	writeRawStore(t, gapPath, 0o600, gappy)
	store, err = Open(gapPath)
	if err != nil {
		t.Fatalf("a store with sequence gaps was refused: %v", err)
	}
	if got := store.Sequence(); got != 9 {
		t.Fatalf("sequence = %d, want the persisted 9", got)
	}
}

// TestOpenToleratesRetiredBoundaryShapesAndRefusesEpochs pins the two raw
// fields' rules after the crash-fencing retirement: the retired orphanBoundary
// key is decoded for tolerance in every shape — a prior-build file must load —
// while a fencing epoch is still an object or an absent optional field, never
// null, an array or a scalar.
func TestOpenToleratesRetiredBoundaryShapesAndRefusesEpochs(t *testing.T) {
	orphan := func(boundary string) string {
		return `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"orphan-unverified","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"orphanBoundary":` + boundary + `}]}`
	}
	pending := func(epoch string) string {
		return `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"fencingEpoch":` + epoch + `}]}`
	}
	tolerated := map[string]string{
		"null boundary":               orphan("null"),
		"object boundary":             orphan("{}"),
		"scalar boundary":             orphan("123"),
		"string boundary":             orphan(`"x"`),
		"member array":                orphan(`[{"host":"h1","kind":"local-linux"}]`),
		"demonstrably empty boundary": orphan("[]"),
	}
	for name, body := range tolerated {
		t.Run("tolerated/"+name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); err != nil {
				t.Fatalf("Open on the prior-build shape %q was refused: %v", name, err)
			}
		})
	}
	refused := map[string]string{
		"null epoch":   pending("null"),
		"array epoch":  pending("[]"),
		"scalar epoch": pending("7"),
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
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			return nil
		}
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

// TestTheFirstWriteSyncsTheDirectoryThatCarriesANewStore pins spec §4's
// durability paragraph on the write path: the parent entry that carries the store
// directory is fsynced (every write, so a directory an earlier attempt created
// before its parent sync failed converges) and so is the directory the rename
// landed in.
func TestTheFirstWriteSyncsTheDirectoryThatCarriesANewStore(t *testing.T) {
	root := t.TempDir()
	path := StorePath(root)
	var synced []string
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		synced = append(synced, dir)
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}

	createTestRecord(t, store, "h1")
	dir := filepath.Dir(path)
	want := []string{filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)), dir}
	if len(synced) != len(want) {
		t.Fatalf("the first write synced %v, want the store chain's parent entries then the directory the rename landed in: %v", synced, want)
	}
	for i := range want {
		if synced[i] != want[i] {
			t.Fatalf("the first write synced %v, want %v", synced, want)
		}
	}

	synced = nil
	createTestRecord(t, store, "h2")
	if len(synced) != len(want) {
		t.Fatalf("a later write synced %v, want the same entries every write: %v", synced, want)
	}
	for i := range want {
		if synced[i] != want[i] {
			t.Fatalf("a later write synced %v, want %v", synced, want)
		}
	}
}

// TestTheFirstWriteSyncsEveryLevelItCreates pins the recursive half: a fresh state
// root needs several levels at once, and each level's parent entry is synced as
// that level is created — bottom-up, a crash before a level's entry is durable
// loses that level, the store and the record with it.
func TestTheFirstWriteSyncsEveryLevelItCreates(t *testing.T) {
	scratch := t.TempDir()
	stateRoot := filepath.Join(scratch, "state", "evener")
	path := StorePath(stateRoot)
	var synced []string
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		synced = append(synced, dir)
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}

	createTestRecord(t, store, "h1")
	dir := filepath.Dir(path)
	// Each created level's parent as that level is created, interleaved with the
	// store chain's parent-entry syncs as the walk bottoms out, then the directory
	// the rename landed in:
	//   creating `state`  -> sync(scratch)
	//   walk for `state`  -> sync(parent of scratch)
	//   creating `evener` -> sync(state)
	//   walk for `evener` -> sync(scratch)
	//   creating `hostops` -> sync(evener)
	//   walk for `hostops` -> sync(state)
	//   the rename        -> sync(hostops)
	want := []string{scratch, filepath.Dir(scratch), filepath.Dir(stateRoot), scratch, stateRoot, filepath.Dir(stateRoot), dir}
	if len(synced) != len(want) {
		t.Fatalf("the write synced %v, want one entry per level (and the store directory): %v", synced, want)
	}
	for i := range want {
		if synced[i] != want[i] {
			t.Fatalf("the write synced %v, want %v", synced, want)
		}
	}
}

// TestLoadNormalizesStoredTimestampsToUTC pins spec §8's storage rule:
// "`createdAt`/`updatedAt` are stored UTC-normalized (`Z`-suffixed RFC3339; a
// stored offset form converts at write time)". A file that arrived with an offset
// (hand-edited, migrated or custody-imported) is normalized on load, so the
// offset never survives into a later write, and the instant is unchanged.
func TestLoadNormalizesStoredTimestampsToUTC(t *testing.T) {
	path := StorePath(t.TempDir())
	body := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T02:00:00+02:00","updatedAt":"2026-09-26T03:00:00+02:00","hostRemoved":false}]}`
	writeRawStore(t, path, 0o600, body)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stored, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatalf("record not loaded")
	}
	if got := stored.CreatedAt.Location(); got != time.UTC {
		t.Fatalf("loaded createdAt location = %v, want UTC", got)
	}
	if want := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC); !stored.CreatedAt.Equal(want) {
		t.Fatalf("loaded createdAt = %v, want the same instant as %v", stored.CreatedAt, want)
	}

	if _, err := store.Transition(stored.ID, StateRunning, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	raw := string(mustReadFile(t, path))
	if !strings.Contains(raw, `"createdAt":"2026-09-26T00:00:00Z"`) {
		t.Fatalf("the write did not store createdAt UTC-normalized:\n%s", raw)
	}
	if strings.Contains(raw, "+02:00") {
		t.Fatalf("the offset form survived into the store file:\n%s", raw)
	}
}

// TestOpenAcceptsARecordCarryingEveryRequiredField is the positive control for
// the presence rules: a terminal record with its host-removed mark and a result
// that carries both halves loads.
func TestOpenAcceptsARecordCarryingEveryRequiredField(t *testing.T) {
	body := `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":true,"result":{"ok":false,"message":"waitHealthy timed out"},"sequence":1}]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, body)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stored, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatalf("record not loaded")
	}
	if !stored.HostRemoved {
		t.Fatalf("host-removed mark lost: %+v", stored)
	}
	if stored.Result == nil || stored.Result.OK || stored.Result.Message != "waitHealthy timed out" {
		t.Fatalf("terminal result lost: %+v", stored.Result)
	}
}

// TestOpenRefusesEveryRequiredRecordFieldThatIsOmitted sweeps the record's
// required fields for the one class of decode gap the top level and the record
// booleans had: a field whose omitted value decodes to a value that is itself
// legitimate, so a malformed or truncated file would pass validation and be
// rewritten in that shape. Every field below is one the writer always emits; a
// file that drops any of them is refused, and refused without being rewritten.
//
// The fields not listed are optional by construction and absent is meaningful for
// them: `fencingEpoch` (the create write lands before the first running probe
// sets it), `orphanBoundary` (paired with the orphan-unverified state),
// `progress` (a record may carry no progress yet) and `result` (absent until the
// record is terminal). Their present forms are shape-checked separately.
func TestOpenRefusesEveryRequiredRecordFieldThatIsOmitted(t *testing.T) {
	complete := `{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy",` +
		`"state":"complete","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z",` +
		`"updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"result":{"ok":true,"message":"deployed"},"sequence":1}`
	omitted := map[string]string{
		"id":                `"id":"00000000000000000001",`,
		"clientOperationId": `"clientOperationId":"client-h1",`,
		"host":              `"host":"h1",`,
		"kind":              `"kind":"deploy",`,
		"state":             `"state":"complete",`,
		"generation":        `"generation":7,`,
		"incarnationId":     `"incarnationId":"inc-1",`,
		"createdAt":         `"createdAt":"2026-09-26T00:00:00Z",`,
		"updatedAt":         `"updatedAt":"2026-09-26T00:00:00Z",`,
		"hostRemoved":       `"hostRemoved":false,`,
		"sequence":          `,"sequence":1`,
		"result outcome":    `"ok":true,`,
		"result message":    `,"message":"deployed"`,
	}
	for name, fragment := range omitted {
		t.Run(name, func(t *testing.T) {
			record := strings.Replace(complete, fragment, "", 1)
			if record == complete {
				t.Fatalf("the fixture for %s did not change the record", name)
			}
			body := `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` + record + `]}`
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if _, err := Open(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("a record with no %s loaded: err = %v, want ErrStoreCorrupt", name, err)
			}
			if got := string(mustReadFile(t, path)); got != body {
				t.Fatalf("a refused load rewrote the file with no %s", name)
			}
		})
	}
}

// TestOpenRevalidatesTheOwnerOnlyModeOfAnAlreadyHeldStore pins the cached-open
// half of §4's "Startup refuses to load a store readable beyond its owner": a
// cached cell makes no load, but Open still refuses a handle for a store file
// that has become readable beyond its owner since the cell was created, so the
// rule reads the same whether or not a handle is already held.
func TestOpenRevalidatesTheOwnerOnlyModeOfAnAlreadyHeldStore(t *testing.T) {
	path := StorePath(t.TempDir())
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	createTestRecord(t, first, "h1")
	if _, err := Open(path); err != nil {
		t.Fatalf("second Open of a healthy store: %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod(%s, 0644): %v", path, err)
	}
	if _, err := Open(path); !errors.Is(err, ErrStoreReadableBeyondOwner) {
		t.Fatalf("Open on a widened store file with a held cell: err = %v, want ErrStoreReadableBeyondOwner", err)
	}
}

// TestCreateAndTransitionRefuseValuesTheStoreCannotWriteBack pins the two value
// rules the writer's encoder imposes: a result is terminal data, so a record that
// has not finished cannot carry one, and every persisted string must be valid
// UTF-8, because encoding/json would write U+FFFD in place of invalid bytes and
// the reloaded value would differ from the one the caller handed in.
func TestCreateAndTransitionRefuseValuesTheStoreCannotWriteBack(t *testing.T) {
	t.Run("create with an invalid utf-8 host", func(t *testing.T) {
		store, path := openTestStore(t)
		createTestRecord(t, store, "h1")
		before := mustReadFile(t, path)
		if _, err := store.Create(NewRecord{
			ClientOperationID: "client-h1", Host: "h\xff\xfe", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
		}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Create with an invalid UTF-8 host: err = %v, want ErrInvalidRecord", err)
		}
		if got := string(mustReadFile(t, path)); got != string(before) {
			t.Fatalf("a refused Create rewrote the store file")
		}
	})
	t.Run("create with an invalid utf-8 client operation id", func(t *testing.T) {
		store, _ := openTestStore(t)
		if _, err := store.Create(NewRecord{
			ClientOperationID: "client-\xff\xfe", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
		}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Create with an invalid UTF-8 client operation id: err = %v, want ErrInvalidRecord", err)
		}
	})
	t.Run("result on a non-terminal transition", func(t *testing.T) {
		store, _ := openTestStore(t)
		record := createTestRecord(t, store, "h1")
		if _, err := store.Transition(record.ID, StateRunning, func(r *Record) {
			r.Result = &Result{OK: true, Message: "deployed"}
		}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Transition into running carrying a result: err = %v, want ErrInvalidRecord", err)
		}
		if stored, _ := store.Record(record.ID); stored.Result != nil || stored.State != StatePending {
			t.Fatalf("the refused transition mutated the record: %+v", stored)
		}
	})
	t.Run("invalid utf-8 result message", func(t *testing.T) {
		store, _ := openTestStore(t)
		record := createTestRecord(t, store, "h1")
		if _, err := store.Transition(record.ID, StateFailed, func(r *Record) {
			r.Result = &Result{OK: false, Message: "boom \xff\xfe"}
		}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Transition with an invalid UTF-8 result: err = %v, want ErrInvalidRecord", err)
		}
	})
	t.Run("invalid utf-8 progress entry", func(t *testing.T) {
		store, _ := openTestStore(t)
		record := createTestRecord(t, store, "h1")
		if _, err := store.Transition(record.ID, StateRunning, func(r *Record) {
			r.Progress = []ProgressEntry{{TS: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Message: "pushed \xff\xfe"}}
		}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Transition with an invalid UTF-8 progress entry: err = %v, want ErrInvalidRecord", err)
		}
	})
}

// TestTransitionRefusesARawFieldThatIsNotValidUTF8 pins the raw fields' half of the
// UTF-8 rule: they are written verbatim, so invalid bytes in one would land in the
// file and the next load would refuse the store this very call wrote.
func TestTransitionRefusesARawFieldThatIsNotValidUTF8(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	createTestRecord(t, store, "h2")
	before := mustReadFile(t, path)

	if _, err := store.Transition(record.ID, StateRunning, func(r *Record) {
		r.FencingEpoch = json.RawMessage(`{"bootId":"` + "\xff\xfe" + `","opSeq":3}`)
	}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Transition with an invalid UTF-8 fencing epoch: err = %v, want ErrInvalidRecord", err)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused transition rewrote the store file")
	}
	// The store the refusals protected still loads.
	reopenFresh(t, path)
}

// TestValidateRawFieldKeys pins the raw-field walk's state machine: a duplicate
// key anywhere in an opaque field is refused (the bytes are written verbatim, so
// the file must mean one thing), while the keys themselves are not the store's to
// judge — a foreign schema's own field names pass.
func TestValidateRawFieldKeys(t *testing.T) {
	refused := []string{
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`[{"a":1,"a":2}]`,
		`{"a":[{"b":"x","b":"y"}]}`,
		`{"a":"x","a":"y"}`,
	}
	for _, body := range refused {
		if err := validateRawFieldKeys(json.RawMessage(body)); err == nil {
			t.Fatalf("duplicate key in %s was accepted", body)
		}
	}
	accepted := []string{
		`{}`,
		`[]`,
		`{"bootId":"a","opSeq":3}`,
		`{"a":{"b":1},"c":{"b":2}}`,
		`{"a":["x","x"],"b":true,"c":null}`,
		`[{"host":"h1","kind":"local-linux"},{"host":"h2"}]`,
	}
	for _, body := range accepted {
		if err := validateRawFieldKeys(json.RawMessage(body)); err != nil {
			t.Fatalf("clean raw field %s was refused: %v", body, err)
		}
	}
}

// TestValidateStoreKeysRefusesKeysTheStoreNeverWrites pins the canonical-name half
// of the file walk. encoding/json matches struct fields case-insensitively, so
// `Records` or `Host` would decode into the canonical fields and be rewritten in
// their spelling, and two case variants of one name could overwrite each other.
func TestValidateStoreKeysRefusesKeysTheStoreNeverWrites(t *testing.T) {
	refused := []string{
		`{"Version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":0,"Records":[]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(recordJSON("00000000000000000001", "pending"), `"host":"h1"`, `"Host":"h1"`, 1) + `]}`,
		`{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
			strings.Replace(terminalRecordJSON("00000000000000000001", 1), `"hostRemoved":false`, `"hostRemoved":false,"result":{"OK":true,"message":"x"}`, 1) + `]}`,
	}
	for _, body := range refused {
		if err := validateStoreKeys([]byte(body)); err == nil {
			t.Fatalf("non-canonical key in %s was accepted", body)
		}
	}
	accepted := []string{
		validStoreJSON,
	}
	for _, body := range accepted {
		if err := validateStoreKeys([]byte(body)); err != nil {
			t.Fatalf("clean store %s was refused: %v", body, err)
		}
	}
}

// TestTransitionRefusesARawFieldThatNamesAKeyTwice pins the write-path half of the
// duplicate-key rule: the loader refuses a file that names any key twice, so the
// write path must refuse a raw field that does — otherwise a transition would
// commit a file the next boot cannot load, bricking the store.
func TestTransitionRefusesARawFieldThatNamesAKeyTwice(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	before := mustReadFile(t, path)

	if _, err := store.Transition(record.ID, StateRunning, func(r *Record) {
		r.FencingEpoch = json.RawMessage(`{"bootId":"a","bootId":"b","opSeq":3}`)
	}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Transition with a duplicated fencing-epoch key: err = %v, want ErrInvalidRecord", err)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused transition rewrote the store file")
	}
	// The store the refusals protected still loads.
	reopenFresh(t, path)
}

// TestOpenAcceptsAProgressListAndRefusesItsAbsenceInAWrittenShape pins the progress
// field's presence rule from both sides: an entry list and an empty list are the
// shapes the writer produces, a present null is not.
func TestOpenAcceptsAProgressListAndRefusesItsAbsenceInAWrittenShape(t *testing.T) {
	withProgress := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"running",` +
		`"generation":7,"incarnationId":"inc-1","progress":[{"ts":"2026-09-26T12:00:00Z","message":"pushed"}],` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, withProgress)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a store carrying progress: %v", err)
	}
	stored, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatalf("record not loaded")
	}
	if len(stored.Progress) != 1 || stored.Progress[0].Message != "pushed" {
		t.Fatalf("progress lost on load: %+v", stored.Progress)
	}

	empty := strings.Replace(withProgress, `"progress":[{"ts":"2026-09-26T12:00:00Z","message":"pushed"}]`, `"progress":[]`, 1)
	emptyPath := StorePath(t.TempDir())
	writeRawStore(t, emptyPath, 0o600, empty)
	if _, err := Open(emptyPath); err != nil {
		t.Fatalf("Open on a store carrying an empty progress list: %v", err)
	}
}

// TestTheChainSyncsConvergeAfterAFailedAttempt pins the reason the parent-entry
// syncs repeat on every write rather than only on creation: a write whose sync
// failed may already have created a level, and only repeating the syncs lets that
// level's entry become durable.
func TestTheChainSyncsConvergeAfterAFailedAttempt(t *testing.T) {
	root := t.TempDir()
	path := StorePath(root)
	var synced []string
	failing := filepath.Dir(root)
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, dir string) error {
		synced = append(synced, dir)
		if dir == failing {
			return errors.New("directory sync fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}

	if _, err := store.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	}); err == nil {
		t.Fatalf("a write whose chain sync failed reported success")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write left the store file behind")
	}

	synced = nil
	failing = ""
	if _, err := store.Create(NewRecord{
		ClientOperationID: "client-h1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-1",
	}); err != nil {
		t.Fatalf("Create after the fault cleared: %v", err)
	}
	found := false
	for _, dir := range synced {
		if dir == filepath.Dir(root) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the retry synced %v, want the level whose entry the failed attempt left unsynced (%q)", synced, filepath.Dir(root))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the retry did not land the store file: %v", err)
	}
}

// TestOpenAcceptsAValidSurrogatePairAndRefusesALoneOne pins the surrogate rule from
// both sides: a paired escape is an astral character and loads as the value the file
// denotes, while an unpaired one is refused because the decoder would silently
// replace it with U+FFFD and the next write would persist that replacement.
func TestOpenAcceptsAValidSurrogatePairAndRefusesALoneOne(t *testing.T) {
	pair := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"a` + `\ud83d\ude00` + `b","kind":"deploy","state":"pending",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, pair)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a store carrying a surrogate pair: %v", err)
	}
	stored, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatalf("record not loaded")
	}
	if want := "a\U0001F600b"; stored.Host != want {
		t.Fatalf("host = %q, want the astral character %q", stored.Host, want)
	}

	// A backslash escaping a backslash is text, not an escape: \\ud800 is fine.
	literal := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"a` + `\\ud800` + `b","kind":"deploy","state":"pending",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
	literalPath := StorePath(t.TempDir())
	writeRawStore(t, literalPath, 0o600, literal)
	if _, err := Open(literalPath); err != nil {
		t.Fatalf("Open on a store carrying an escaped backslash before ud800: %v", err)
	}
}

// TestTransitionRefusesALoneSurrogateInARawField pins the write path against the
// loader's own byte rules: a raw field carrying a \u escape for an unpaired
// surrogate would be written verbatim and the next load would refuse the file the
// store just committed, so the write refuses first — and the store still loads
// afterwards.
func TestTransitionRefusesALoneSurrogateInARawField(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	createTestRecord(t, store, "h2")
	before := mustReadFile(t, path)

	escape := json.RawMessage(`{"bootId":"\ud800","opSeq":3}`)
	if _, err := store.Transition(record.ID, StateRunning, func(r *Record) {
		r.FencingEpoch = escape
	}); err == nil {
		t.Fatalf("a write carrying a lone surrogate escape in a raw field succeeded")
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused write rewrote the store file")
	}
	reopenFresh(t, path)
}

// TestACachedOpenDoesNotServeAVanishedStoreFile pins the cell's file accounting: a
// store file removed, or renamed aside by a quarantine, must not keep being served
// from the cached cell — the durable store is what the file says, and there is no
// file — and the next write must land a fresh store rather than recreate the
// deleted one from a stale snapshot.
func TestACachedOpenDoesNotServeAVanishedStoreFile(t *testing.T) {
	path := StorePath(t.TempDir())
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	createTestRecord(t, first, "h1")
	if got := len(first.Records()); got != 1 {
		t.Fatalf("the first handle holds %d records, want 1", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(%s): %v", path, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after the file vanished: %v", err)
	}
	if got := len(reopened.Records()); got != 0 {
		t.Fatalf("a cached open served %d records from a store file that is gone", got)
	}
	if got := reopened.Sequence(); got != 0 {
		t.Fatalf("a cached open served sequence %d from a store file that is gone", got)
	}

	record := createTestRecord(t, reopened, "h2")
	if got := len(reopened.Records()); got != 1 || reopened.Records()[0].ID != record.ID {
		t.Fatalf("the write after the file vanished did not start a fresh store: %+v", reopened.Records())
	}
	fresh := reopenFresh(t, path)
	if got := len(fresh.Records()); got != 1 {
		t.Fatalf("the fresh store file holds %d records, want 1", got)
	}
}

// TestRepeatedOpensOfANeverWrittenStoreStayEmpty pins the other side: a store that
// was never written has no file, and repeated opens of it are legitimately empty
// rather than a vanished store.
func TestRepeatedOpensOfANeverWrittenStoreStayEmpty(t *testing.T) {
	path := StorePath(t.TempDir())
	for i := range 3 {
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if got := len(store.Records()); got != 0 {
			t.Fatalf("open %d saw %d records in a never-written store", i, got)
		}
	}
}
