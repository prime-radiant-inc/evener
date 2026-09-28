package hostops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// absurdHWMTerminalRecordJSON is one terminal record stamped above the store's
// own sequence so the file is schema-invalid while its record set stays whole:
// the shape the custody extraction has to account for before anything scales by
// the file's allocator high-water mark.
func absurdHWMTerminalRecordJSON() string {
	return `{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}`
}

// TestQuarantineRefusesAnAbsurdAllocatorHighWaterMark pins the high finding: the
// completeness accounting must never iterate a value read from the corrupt file.
// An allocator high-water mark of 10^12 or MaxUint64 resolves promptly to an
// incomplete-custody refusal — no hang, no spin, no overflow wrap.
func TestQuarantineRefusesAnAbsurdAllocatorHighWaterMark(t *testing.T) {
	for name, hwm := range map[string]string{
		"a trillion": strconv.FormatUint(1_000_000_000_000, 10),
		"max uint64": strconv.FormatUint(^uint64(0), 10),
	} {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			body := `{"version":1,"sequence":1,"allocatorHighWaterMark":` + hwm + `,"records":[` + absurdHWMTerminalRecordJSON() + `]}`
			writeRawStore(t, path, 0o600, body)

			type result struct {
				store *Store
				err   error
			}
			done := make(chan result, 1)
			go func() {
				store, err := Open(path)
				done <- result{store: store, err: err}
			}()
			select {
			case got := <-done:
				if got.store != nil {
					t.Fatalf("Open returned a handle for an absurd high-water mark")
				}
				if !errors.Is(got.err, ErrStoreCorrupt) || !errors.Is(got.err, ErrQuarantineIncomplete) {
					t.Fatalf("Open = %v, want an ErrStoreCorrupt/ErrQuarantineIncomplete refusal", got.err)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("Open on a file claiming allocatorHighWaterMark %s did not return: the completeness check iterated the file-controlled value", hwm)
			}
			for _, infix := range []string{".quarantined-", ".custody-"} {
				entries, err := os.ReadDir(filepath.Dir(path))
				if err != nil {
					t.Fatalf("ReadDir: %v", err)
				}
				for _, entry := range entries {
					if strings.Contains(entry.Name(), infix) {
						t.Fatalf("a refused quarantine wrote %s", entry.Name())
					}
				}
			}
		})
	}
}

// TestNextControllerIDsRefuseAnOverflow pins the second half of the high
// finding: allocating the ownership-only ids above a base must refuse to wrap,
// never mint a zero or reused id.
func TestNextControllerIDsRefuseAnOverflow(t *testing.T) {
	if _, err := nextControllerIDs(^uint64(0), 1); err == nil {
		t.Fatalf("nextControllerIDs(MaxUint64, 1) succeeded, want an overflow refusal")
	}
	if _, err := nextControllerIDs(^uint64(0)-1, 2); err == nil {
		t.Fatalf("nextControllerIDs(MaxUint64-1, 2) succeeded, want an overflow refusal")
	}
	ids, err := nextControllerIDs(3, 2)
	if err != nil {
		t.Fatalf("nextControllerIDs(3, 2): %v", err)
	}
	if len(ids) != 2 || ids[0] != formatAllocatorID(4) || ids[1] != formatAllocatorID(5) {
		t.Fatalf("nextControllerIDs(3, 2) = %v, want the two ids above 3", ids)
	}
}

// TestOperationsReadWorksOnAFreshlyQuarantinedStore pins the second high
// finding: the replacement store's imported hosts carry no mirrored boundary,
// and every operations query must still work — unfiltered, host-pinned and the
// id detail filter — with the imported records listed under their own pinned
// pair, and continuations staying pinned to it.
func TestOperationsReadWorksOnAFreshlyQuarantinedStore(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	unfiltered, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("unfiltered read on a freshly quarantined store: %v", err)
	}
	if len(unfiltered.Records) != 2 {
		t.Fatalf("unfiltered read listed %d records, want the two custody imports: %+v", len(unfiltered.Records), unfiltered.Records)
	}
	bound, ok := unfiltered.HostBoundaries["h1"]
	if !ok || bound.Absent || bound.Boundary.Generation != 3 || bound.Boundary.IncarnationID != "inc-h1" {
		t.Fatalf("the unfiltered page's hostBoundaries[h1] = %+v, want the imported record's own pair", bound)
	}
	if bound.Boundary.PresenceEpoch != 0 {
		t.Fatalf("the synthesized bounds entry carries presenceEpoch %d, want the documented 0 placeholder", bound.Boundary.PresenceEpoch)
	}

	pinned, err := store.ReadOperations(OperationsQuery{Host: "h1"})
	if err != nil {
		t.Fatalf("host-pinned read on a freshly quarantined store: %v", err)
	}
	if len(pinned.Records) != 1 || pinned.Records[0].ID != "00000000000000000001" {
		t.Fatalf("host-pinned read = %+v, want the h1 fence import", pinned.Records)
	}
	detail, err := store.ReadOperations(OperationsQuery{ID: "00000000000000000003"})
	if err != nil {
		t.Fatalf("id detail read on a freshly quarantined store: %v", err)
	}
	if len(detail.Records) != 1 || detail.Records[0].Host != "h2" {
		t.Fatalf("id detail read = %+v, want the h2 ownership import", detail.Records)
	}

	// The continuation stays pinned to the synthesized entry, and a mirror
	// landing later moves the host's entry and refuses it.
	first, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("no cursor to continue with")
	}
	if _, err := store.ReadOperations(OperationsQuery{Limit: 1, Cursor: first.NextCursor}); err != nil {
		t.Fatalf("continuation under the synthesized entry: %v", err)
	}
	mirrorCursorBoundary(t, store, "h1", 3, "inc-h1", 4)
	if _, err := store.ReadOperations(OperationsQuery{Limit: 1, Cursor: first.NextCursor}); err == nil {
		t.Fatal("a continuation minted before the mirror landed was admitted after it")
	} else if _, ok := errors.AsType[*CursorStaleError](err); !ok {
		t.Fatalf("continuation after the mirror landed = %v, want a typed stale-entry refusal", err)
	}
}

// TestConcurrentFirstOpensQuarantineOnce pins the race finding: two opens of one
// corrupt store serialize, so exactly one quarantine happens and every handle
// serves the same imported record set.
func TestConcurrentFirstOpensQuarantineOnce(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}

	const opens = 8
	stores := make([]*Store, opens)
	errs := make([]error, opens)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range opens {
		wg.Go(func() {
			<-start
			stores[i], errs[i] = openFS(afero.NewOsFs(), path, storeFaults{})
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racing open %d: %v", i, err)
		}
		if stores[i] == nil {
			t.Fatalf("racing open %d returned no store", i)
		}
		if _, ok := stores[i].Record("00000000000000000001"); !ok {
			t.Fatalf("racing open %d serves a store without the fence import: %+v", i, stores[i].Records())
		}
		if got := stores[i].CursorEpoch().QuarantineEpoch; got != 1 {
			t.Fatalf("racing open %d reads epoch %d, want 1", i, got)
		}
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var asides, custodies int
	for _, entry := range entries {
		switch {
		case strings.Contains(entry.Name(), ".quarantined-"):
			asides++
		case strings.Contains(entry.Name(), ".custody-"):
			custodies++
		case strings.Contains(entry.Name(), ".quarantine-intent."):
			t.Fatalf("the racing opens left an intent behind: %v", entries)
		}
	}
	if asides != 1 || custodies != 1 {
		t.Fatalf("racing opens left %d asides and %d custody files, want exactly one each: %v", asides, custodies, entries)
	}
}

// TestQuarantineRefusesNonRegularArtifacts pins the symlink findings: a
// symlinked store path, sidecar, intent or custody file is refused without
// following it, and no quarantine artifact is written for a path that is not a
// regular store file.
func TestQuarantineRefusesNonRegularArtifacts(t *testing.T) {
	t.Run("symlinked store path", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		target := filepath.Join(dir, "elsewhere.json")
		writeRawStore(t, target, 0o600, quarantinedStoreJSON)
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open through a symlinked store path returned %v, want a refusal", store)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".quarantined-") || strings.Contains(entry.Name(), ".custody-") {
				t.Fatalf("a refused quarantine left %s behind", entry.Name())
			}
		}
	})

	t.Run("symlinked epoch sidecar", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		elsewhere := filepath.Join(dir, "elsewhere-epoch.json")
		if err := os.WriteFile(elsewhere, []byte(`{"quarantineEpoch":4}`), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Symlink(elsewhere, quarantineEpochPath(path)); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a symlinked epoch sidecar returned %v, want a refusal", store)
		}
	})

	t.Run("symlinked intent", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		target := filepath.Join(dir, "elsewhere-intent.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Symlink(target, quarantineIntentPath(path)); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a symlinked intent returned %v, want a refusal", store)
		}
	})

	t.Run("symlinked custody in a recovery state", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("Open: %v", err)
		}
		custodyPath, custodyBytes := quarantineArtifact(t, filepath.Dir(path), ".custody-")
		copyPath := filepath.Join(dir, "custody-copy.json")
		if err := os.WriteFile(copyPath, custodyBytes, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Remove(custodyPath); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := os.Symlink(copyPath, custodyPath); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open over a symlinked custody file returned %v, want a refusal", store)
		}
	})

	t.Run("writeQuarantineFile refuses a symlinked target", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "artifact.json")
		elsewhere := filepath.Join(dir, "elsewhere.json")
		if err := os.WriteFile(elsewhere, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Symlink(elsewhere, target); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := writeQuarantineFile(afero.NewOsFs(), target, []byte(`{"quarantineEpoch":1}`), storeFaults{}); err == nil {
			t.Fatalf("writeQuarantineFile replaced a symlinked target, want a refusal")
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatalf("Lstat: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the refused write replaced the symlink")
		}
	})
}

// TestQuarantineRefusesArtifactMismatches pins the aside-proof finding: an aside
// that is not a regular file, an intent whose custody binding is not exact, and
// a custody file with neither an aside nor a pending intent all fail closed.
func TestQuarantineRefusesArtifactMismatches(t *testing.T) {
	newQuarantinedDir := func(t *testing.T) (string, string, string) {
		t.Helper()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("Open: %v", err)
		}
		asidePath, _ := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
		custodyPath, _ := quarantineArtifact(t, filepath.Dir(path), ".custody-")
		return path, asidePath, custodyPath
	}

	t.Run("aside that is a directory", func(t *testing.T) {
		path, asidePath, _ := newQuarantinedDir(t)
		if err := os.Remove(asidePath); err != nil {
			t.Fatalf("Remove(aside): %v", err)
		}
		if err := os.Mkdir(asidePath, 0o700); err != nil {
			t.Fatalf("Mkdir(aside): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a directory standing in for the aside returned %v, want a refusal", store)
		}
	})

	t.Run("aside that is a symlink", func(t *testing.T) {
		path, asidePath, _ := newQuarantinedDir(t)
		elsewhere := filepath.Join(filepath.Dir(path), "elsewhere-aside")
		body := mustReadFile(t, asidePath)
		if err := os.WriteFile(elsewhere, body, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Remove(asidePath); err != nil {
			t.Fatalf("Remove(aside): %v", err)
		}
		if err := os.Symlink(elsewhere, asidePath); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a symlinked aside returned %v, want a refusal", store)
		}
	})

	t.Run("intent with a mismatched custody binding", func(t *testing.T) {
		path, asidePath, custodyPath := newQuarantinedDir(t)
		intent := fmt.Sprintf(`{"corruptFile":%q,"custodyFile":%q,"asideFile":%q,"quarantinedAt":%q,"quarantineEpoch":1}`,
			path, custodyPath+"-other.json", asidePath, time.Now().UTC().Format(time.RFC3339Nano))
		if err := os.WriteFile(quarantineIntentPath(path), []byte(intent), 0o600); err != nil {
			t.Fatalf("WriteFile(intent): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a mismatched intent binding returned %v, want a refusal", store)
		}
	})

	t.Run("custody with neither aside nor intent", func(t *testing.T) {
		path, asidePath, _ := newQuarantinedDir(t)
		if err := os.Remove(asidePath); err != nil {
			t.Fatalf("Remove(aside): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a custody file neither aside-backed nor intent-backed returned %v, want a refusal", store)
		}
	})
}

// TestQuarantineForcesTheAsideOwnerOnly pins the low finding: the aside never
// keeps a wider mode than 0600, whatever the corrupt file carried.
func TestQuarantineForcesTheAsideOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o400, quarantinedStoreJSON)
	if _, err := Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	asidePath, aside := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
	if string(aside) != quarantinedStoreJSON {
		t.Fatalf("the aside does not hold the corrupt bytes verbatim")
	}
	info, err := os.Stat(asidePath)
	if err != nil {
		t.Fatalf("Stat(aside): %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("the aside mode = %04o, want 0600", got)
	}
}

// TestSecondQuarantineDoesNotReuseEarlierCustodyIDs pins the cross-quarantine
// allocator regression: a later quarantine whose corrupt file carries a lower
// high-water mark still allocates above every id an earlier custody file
// handed out, so an id-only resolve can never alias an unrelated record.
func TestSecondQuarantineDoesNotReuseEarlierCustodyIDs(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstCustodyPath, firstCustodyBytes := quarantineArtifact(t, filepath.Dir(path), ".custody-")
	var firstCustody struct {
		RecordIDs []struct {
			RecordID string `json:"recordId"`
		} `json:"recordIds"`
	}
	if err := json.Unmarshal(firstCustodyBytes, &firstCustody); err != nil {
		t.Fatalf("decode first custody: %v", err)
	}
	if len(firstCustody.RecordIDs) != 2 {
		t.Fatalf("first custody names %d records, want 2", len(firstCustody.RecordIDs))
	}
	firstIDs := map[string]bool{}
	for _, row := range firstCustody.RecordIDs {
		firstIDs[row.RecordID] = true
	}
	// The second corrupt file claims a high-water mark below the first
	// replacement's allocator and yields two ownership-only names.
	second := `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"op-h8","host":"h8","kind":"deploy","state":"complete",` +
		`"generation":2,"incarnationId":"inc-h8","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` +
		`"boundaries":{"h7":{"generation":5,"incarnationId":"inc-h7","presenceEpoch":2}}}`
	writeRawStore(t, path, 0o600, second)
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	replacement, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	for _, record := range replacement.Records() {
		if firstIDs[record.ID] {
			t.Fatalf("the second quarantine reused id %q, which the first custody file still references (%s): %+v",
				record.ID, firstCustodyPath, replacement.Records())
		}
	}
	state := storeSnapshotForTest(replacement)
	if state.AllocatorHighWaterMark <= 3 {
		t.Fatalf("the replacement allocator high-water mark = %d, want it above the first custody's ids", state.AllocatorHighWaterMark)
	}
	if len(first.Records()) == 0 {
		t.Fatalf("the first store reads empty")
	}
}
