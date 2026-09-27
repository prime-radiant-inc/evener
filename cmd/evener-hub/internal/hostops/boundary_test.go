package hostops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// TestBoundaryMirrorLandsAndSurvivesReload pins the substrate the registry's
// per-host boundary record stands on (registry spec 08 §7: "one record per host
// name — {generation, incarnationId, presenceEpoch} — written in the same
// atomic store writes that mirror the generation"): a mirror write lands the
// triples in the store file, reads answer them back, and a fresh load — the
// process restart the store already models — reads exactly the values the
// write committed.
func TestBoundaryMirrorLandsAndSurvivesReload(t *testing.T) {
	store, path := openTestStore(t)
	want := map[string]Boundary{
		"h1": {Generation: 7, IncarnationID: "inc-h1", PresenceEpoch: 2},
		"h2": {Generation: 3, IncarnationID: "inc-h2", PresenceEpoch: 1},
	}
	if err := store.MirrorBoundaries(want); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	got, ok := store.Boundary("h1")
	if !ok || got != want["h1"] {
		t.Fatalf("Boundary(h1) = %+v, %v; want %+v, true", got, ok, want["h1"])
	}
	reopened := reopenFresh(t, path)
	for name, boundary := range want {
		got, ok := reopened.Boundary(name)
		if !ok || got != boundary {
			t.Fatalf("reloaded Boundary(%s) = %+v, %v; want %+v, true", name, got, ok, boundary)
		}
	}
	if _, ok := reopened.Boundary("absent"); ok {
		t.Fatal("Boundary(absent) reported a boundary for a name that was never mirrored")
	}
}

// TestBoundaryMirrorIsAnUpsert pins the write's shape: mirroring one name
// replaces that name's record and leaves every other mirrored name alone — the
// boundary record is per host, never a whole-map replacement that would drop a
// host the write did not name.
func TestBoundaryMirrorIsAnUpsert(t *testing.T) {
	store, _ := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"h1": {Generation: 7, IncarnationID: "inc-h1", PresenceEpoch: 1},
	}); err != nil {
		t.Fatalf("MirrorBoundaries(h1): %v", err)
	}
	if err := store.MirrorBoundaries(map[string]Boundary{
		"h2": {Generation: 4, IncarnationID: "inc-h2", PresenceEpoch: 5},
	}); err != nil {
		t.Fatalf("MirrorBoundaries(h2): %v", err)
	}
	all := store.Boundaries()
	if len(all) != 2 {
		t.Fatalf("Boundaries() = %v, want both h1 and h2", all)
	}
	if all["h1"] != (Boundary{Generation: 7, IncarnationID: "inc-h1", PresenceEpoch: 1}) {
		t.Fatalf("h1's boundary changed under an unrelated mirror write: %+v", all["h1"])
	}
	// A later mirror for one name advances that name alone.
	if err := store.MirrorBoundaries(map[string]Boundary{
		"h1": {Generation: 8, IncarnationID: "inc-h1", PresenceEpoch: 2},
	}); err != nil {
		t.Fatalf("MirrorBoundaries(h1 again): %v", err)
	}
	all = store.Boundaries()
	if all["h1"] != (Boundary{Generation: 8, IncarnationID: "inc-h1", PresenceEpoch: 2}) {
		t.Fatalf("h1's boundary after its second mirror = %+v", all["h1"])
	}
	if all["h2"] != (Boundary{Generation: 4, IncarnationID: "inc-h2", PresenceEpoch: 5}) {
		t.Fatalf("h2's boundary changed under h1's mirror write: %+v", all["h2"])
	}
}

// TestBoundaryMirrorIsOneWriteForEveryNameItCarries pins the "same write" rule
// on the store side: one mirror call carrying several names lands all of them
// or none of them. The pre-rename fault proves the none arm — not one of the
// two names, never a half-applied mirror — and the post-rename fault proves the
// landed arm commits every name it carried, with the handle's in-memory state
// following the file (RenameLanded).
func TestBoundaryMirrorIsOneWriteForEveryNameItCarries(t *testing.T) {
	path := StorePath(t.TempDir())
	blockRename := true
	store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
		if blockRename {
			return errors.New("before-rename fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	batch := map[string]Boundary{
		"h1": {Generation: 7, IncarnationID: "inc-h1", PresenceEpoch: 1},
		"h2": {Generation: 3, IncarnationID: "inc-h2", PresenceEpoch: 2},
	}
	if err := store.MirrorBoundaries(batch); err == nil {
		t.Fatal("MirrorBoundaries whose rename never landed reported success")
	}
	if got := store.Boundaries(); len(got) != 0 {
		t.Fatalf("a mirror write that never landed left %v in memory, want none", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a mirror write that never landed left the store file behind (stat err = %v)", err)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("a refused mirror write left temp files behind: %v", temps)
	}

	// The post-rename arm: the rename replaced the file, so the write is a
	// landed one even though the directory sync behind it failed — memory must
	// follow the file for every name in the batch, never stay behind it.
	blockRename = false
	if err := store.MirrorBoundaries(map[string]Boundary{"h1": batch["h1"]}); err != nil {
		t.Fatalf("MirrorBoundaries(h1) once the fault cleared: %v", err)
	}
	store.faults.syncDir = func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			// The parent levels ensureStoreDir syncs on every write are not the
			// post-rename step this test drives.
			return nil
		}
		return errors.New("directory sync fault")
	}
	err = store.MirrorBoundaries(map[string]Boundary{"h2": batch["h2"]})
	if err == nil || !RenameLanded(err) {
		t.Fatalf("post-rename mirror failure = %v, want a RenameLanded error", err)
	}
	for name, boundary := range batch {
		got, ok := store.Boundary(name)
		if !ok || got != boundary {
			t.Fatalf("after a landed-rename failure Boundary(%s) = %+v, %v; want %+v, true", name, got, ok, boundary)
		}
	}
	// The write's whole promise is that the mirror is the file's own state: after
	// the landed-rename failure, memory and the file hold the same triples, so a
	// later write cannot rewrite the file from a stale boundary map.
	onDisk := readStoreSnapshot(t, path)
	if len(onDisk.Boundaries) != len(batch) {
		t.Fatalf("file holds %v after the landed mirror write, want %v", onDisk.Boundaries, batch)
	}
	for name, boundary := range batch {
		if onDisk.Boundaries[name] != boundary {
			t.Fatalf("file Boundary(%s) = %+v, memory holds %+v", name, onDisk.Boundaries[name], boundary)
		}
	}
	reopened := reopenFresh(t, path)
	for name, boundary := range batch {
		got, ok := reopened.Boundary(name)
		if !ok || got != boundary {
			t.Fatalf("reloaded Boundary(%s) = %+v, %v; want %+v, true", name, got, ok, boundary)
		}
	}
}

// TestBoundaryMirrorPreservesTheStoresRecords pins that the mirror rides the
// one store file: a store that already holds operation records keeps them, and
// the state-transition sequence and allocator high-water mark, when a boundary
// write lands.
func TestBoundaryMirrorPreservesTheStoresRecords(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if err := store.MirrorBoundaries(map[string]Boundary{
		"h1": {Generation: 7, IncarnationID: "inc-h1", PresenceEpoch: 1},
	}); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	reopened := reopenFresh(t, path)
	if _, ok := reopened.Record(record.ID); !ok {
		t.Fatalf("record %q did not survive the mirror write", record.ID)
	}
	got, ok := reopened.Boundary("h1")
	if !ok || got.IncarnationID != "inc-h1" {
		t.Fatalf("reloaded Boundary(h1) = %+v, %v; want inc-h1", got, ok)
	}
}

// TestStoreFileWithoutBoundariesLoadsEmpty pins the store's own compatibility
// rule: the `boundaries` key arrives with this slice, so a file this store wrote
// before it (the S1 shape, no key at all) must still load, with no boundaries
// mirrored. A present-null key is the same absence — every writer of this store
// emits the key as an object once a boundary lands.
func TestStoreFileWithoutBoundariesLoadsEmpty(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, validStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a store without a boundaries key: %v", err)
	}
	if got := store.Boundaries(); len(got) != 0 {
		t.Fatalf("Boundaries() on a pre-boundary store = %v, want none", got)
	}
	// The old file's bytes must not be rewritten just by opening it.
	if got := string(mustReadFile(t, path)); got != validStoreJSON {
		t.Fatalf("opening a pre-boundary store rewrote it:\n%s", got)
	}

	nullPath := StorePath(t.TempDir())
	writeRawStore(t, nullPath, 0o600, `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"boundaries":null}`)
	nullStore, err := Open(nullPath)
	if err != nil {
		t.Fatalf("Open on a store with a null boundaries key: %v", err)
	}
	if got := nullStore.Boundaries(); len(got) != 0 {
		t.Fatalf("Boundaries() on a null-boundaries store = %v, want none", got)
	}
}

// TestBoundaryMirrorRefusesWhatNoWriterEmits pins the fail-closed schema: a
// mirror write carries complete triples — a non-empty incarnation id (registry
// spec 08 §1: an opaque non-empty string, at most 128 bytes), a positive
// generation, and a positive presence epoch. A refusal commits nothing.
func TestBoundaryMirrorRefusesWhatNoWriterEmits(t *testing.T) {
	longID := strings.Repeat("i", 129)
	cases := map[string]map[string]Boundary{
		"empty name": {
			"": {Generation: 1, IncarnationID: "inc", PresenceEpoch: 1},
		},
		"no generation": {
			"h1": {Generation: 0, IncarnationID: "inc", PresenceEpoch: 1},
		},
		"no incarnation id": {
			"h1": {Generation: 1, PresenceEpoch: 1},
		},
		"no presence epoch": {
			"h1": {Generation: 1, IncarnationID: "inc"},
		},
		"incarnation id over 128 bytes": {
			"h1": {Generation: 1, IncarnationID: longID, PresenceEpoch: 1},
		},
		"incarnation id not UTF-8": {
			"h1": {Generation: 1, IncarnationID: string([]byte{0xff, 0xfe}), PresenceEpoch: 1},
		},
		"name not UTF-8": {
			string([]byte{0xff}): {Generation: 1, IncarnationID: "inc", PresenceEpoch: 1},
		},
	}
	for name, batch := range cases {
		t.Run(name, func(t *testing.T) {
			store, path := openTestStore(t)
			if err := store.MirrorBoundaries(batch); err == nil {
				t.Fatalf("MirrorBoundaries(%s) succeeded, want a refusal", name)
			}
			if got := store.Boundaries(); len(got) != 0 {
				t.Fatalf("a refused mirror write left %v in memory, want none", got)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused mirror write created the store file (stat err = %v)", err)
			}
		})
	}
}
