package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

func openAttachedHostsStore(t *testing.T, stateRoot string) *AttachedHostsStore {
	t.Helper()
	store, err := NewAttachedHostsStore(stateRoot)
	if err != nil {
		t.Fatalf("NewAttachedHostsStore(%q): %v", stateRoot, err)
	}
	if store == nil {
		t.Fatalf("NewAttachedHostsStore(%q) = nil, want store", stateRoot)
	}
	return store
}

func openAttachedHostsStoreFS(t *testing.T, fs afero.Fs, stateRoot string, faults attachedHostsStoreFaults) *AttachedHostsStore {
	t.Helper()
	store, err := newAttachedHostsStoreFS(fs, stateRoot, faults)
	if err != nil {
		t.Fatalf("newAttachedHostsStoreFS(%q): %v", stateRoot, err)
	}
	if store == nil {
		t.Fatalf("newAttachedHostsStoreFS(%q) = nil, want store", stateRoot)
	}
	return store
}

func wantAttachedHosts(t *testing.T, store *AttachedHostsStore, want map[string]AttachedHostRecord) {
	t.Helper()
	if got := store.Snapshot(); !maps.Equal(got, want) {
		t.Fatalf("Snapshot() = %v, want %v", got, want)
	}
}

func TestAttachedHostsStoreMissingFileStartsEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := openAttachedHostsStore(t, root)
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{})
	if _, err := os.Stat(attachedHostsStatePath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file stat error = %v, want not exist before first write", err)
	}
}

func TestAttachedHostsStoreRecordRemoveReload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := openAttachedHostsStore(t, root)
	want := AttachedHostRecord{Generation: 2, IncarnationID: "inc-1"}
	if err := store.Record("alpha", want); err != nil {
		t.Fatalf("Record: %v", err)
	}

	data, err := os.ReadFile(attachedHostsStatePath(root))
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	var envelope struct {
		Version uint64 `json:"version"`
		Hosts   map[string]struct {
			Generation    uint64 `json:"generation"`
			IncarnationID string `json:"incarnation_id"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("persisted state is not JSON: %v", err)
	}
	persisted, ok := envelope.Hosts["alpha"]
	if envelope.Version != 1 || !ok || len(envelope.Hosts) != 1 ||
		persisted.Generation != want.Generation || persisted.IncarnationID != want.IncarnationID {
		t.Fatalf("persisted state = version %d hosts %+v, want version 1 with alpha=%+v",
			envelope.Version, envelope.Hosts, want)
	}

	reloaded := openAttachedHostsStore(t, root)
	wantAttachedHosts(t, reloaded, map[string]AttachedHostRecord{"alpha": want})

	snapshot := reloaded.Snapshot()
	snapshot["alpha"] = AttachedHostRecord{Generation: 9, IncarnationID: "mutated"}
	snapshot["beta"] = AttachedHostRecord{Generation: 1, IncarnationID: "added"}
	wantAttachedHosts(t, reloaded, map[string]AttachedHostRecord{"alpha": want})

	if err := reloaded.Remove("alpha"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	wantAttachedHosts(t, openAttachedHostsStore(t, root), map[string]AttachedHostRecord{})
}

func TestAttachedHostsStoreIdempotentNoOpWrites(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	writes := 0
	store := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{
		BeforeRename: func() error {
			writes++
			return nil
		},
	})
	rec := AttachedHostRecord{Generation: 1, IncarnationID: "inc-1"}
	if err := store.Record("alpha", rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	before, err := afero.ReadFile(fs, attachedHostsStatePath(root))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if err := store.Record("alpha", rec); err != nil {
		t.Fatalf("repeated Record: %v", err)
	}
	if err := store.Remove("missing"); err != nil {
		t.Fatalf("Remove absent: %v", err)
	}
	if err := store.Prune(func(string, AttachedHostRecord) bool { return true }); err != nil {
		t.Fatalf("keep-all Prune: %v", err)
	}
	if writes != 1 {
		t.Fatalf("writes = %d, want 1 (only the first Record)", writes)
	}
	after, err := afero.ReadFile(fs, attachedHostsStatePath(root))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("state changed without a write: before=%q after=%q", before, after)
	}

	if err := store.Record("alpha", AttachedHostRecord{Generation: 2, IncarnationID: "inc-1"}); err != nil {
		t.Fatalf("changed Record: %v", err)
	}
	if writes != 2 {
		t.Fatalf("writes = %d after changed record, want 2", writes)
	}
}

func TestAttachedHostsStorePruneAtomic(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	writes := 0
	store := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{
		BeforeRename: func() error {
			writes++
			return nil
		},
	})
	records := map[string]AttachedHostRecord{
		"alpha": {Generation: 1, IncarnationID: "inc-a"},
		"beta":  {Generation: 2, IncarnationID: "inc-b"},
		"gamma": {Generation: 3, IncarnationID: "inc-c"},
	}
	for name, rec := range records {
		if err := store.Record(name, rec); err != nil {
			t.Fatalf("Record(%q): %v", name, err)
		}
	}
	before := writes

	if err := store.Prune(nil); err != nil {
		t.Fatalf("Prune(nil): %v", err)
	}
	wantAttachedHosts(t, store, records)
	if writes != before {
		t.Fatalf("Prune(nil) wrote %d times, want no write", writes-before)
	}

	if err := store.Prune(func(name string, _ AttachedHostRecord) bool { return name != "beta" }); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if writes != before+1 {
		t.Fatalf("Prune wrote %d times, want 1", writes-before)
	}
	want := map[string]AttachedHostRecord{"alpha": records["alpha"], "gamma": records["gamma"]}
	wantAttachedHosts(t, store, want)
	wantAttachedHosts(t, openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{}), want)
}

func TestAttachedHostsStoreRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := openAttachedHostsStore(t, root)
	for _, name := range []string{"", " ", " \t\n "} {
		if err := store.Record(name, AttachedHostRecord{Generation: 1, IncarnationID: "inc"}); err == nil {
			t.Fatalf("Record(%q) error = nil, want non-nil", name)
		}
		if err := store.Remove(name); err != nil {
			t.Fatalf("Remove(%q) error = %v, want nil", name, err)
		}
	}
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{})
	if _, err := os.Stat(attachedHostsStatePath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected names created state: %v", err)
	}

	rec := AttachedHostRecord{Generation: 3, IncarnationID: "inc-3"}
	if err := store.Record("  alpha\t", rec); err != nil {
		t.Fatalf("Record with padded name: %v", err)
	}
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{"alpha": rec})
	if err := store.Remove(" alpha "); err != nil {
		t.Fatalf("Remove with padded name: %v", err)
	}
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{})
	wantAttachedHosts(t, openAttachedHostsStore(t, root), map[string]AttachedHostRecord{})
}

func TestAttachedHostsStoreCorruptFileUntouched(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
	}{
		{name: "malformed JSON", data: `{"version":1,"hosts":`},
		{name: "unknown field", data: `{"version":1,"hosts":{},"extra":true}`},
		{name: "trailing JSON value", data: `{"version":1,"hosts":{}} {}`},
		{name: "missing hosts", data: `{"version":1}`},
		{name: "null hosts", data: `{"version":1,"hosts":null}`},
		{name: "empty host key", data: `{"version":1,"hosts":{"":{"generation":1,"incarnation_id":"inc"}}}`},
		{name: "untrimmed host key", data: `{"version":1,"hosts":{" alpha":{"generation":1,"incarnation_id":"inc"}}}`},
		{name: "version zero", data: `{"version":0,"hosts":{}}`},
		{name: "unsupported version", data: `{"version":2,"hosts":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := attachedHostsStatePath(root)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("create state dir: %v", err)
			}
			original := []byte(tc.data)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatalf("write corrupt state: %v", err)
			}

			store, err := NewAttachedHostsStore(root)
			if err == nil || store != nil {
				t.Fatalf("NewAttachedHostsStore = (%v, %v), want (nil, error)", store, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read state after failed open: %v", err)
			}
			if !bytes.Equal(after, original) {
				t.Fatalf("corrupt state rewritten: before=%q after=%q", original, after)
			}
		})
	}
}

func TestAttachedHostsStoreAtomicWriteFaults(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected rename fault")
	oldRec := AttachedHostRecord{Generation: 1, IncarnationID: "inc-old"}
	newRec := AttachedHostRecord{Generation: 2, IncarnationID: "inc-new"}

	t.Run("before rename keeps prior state", func(t *testing.T) {
		t.Parallel()
		fs := afero.NewMemMapFs()
		root := "/hub-state"
		seed := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{})
		if err := seed.Record("alpha", oldRec); err != nil {
			t.Fatalf("seed Record: %v", err)
		}

		failing := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{
			BeforeRename: func() error { return failure },
		})
		if err := failing.Record("beta", newRec); !errors.Is(err, failure) {
			t.Fatalf("Record error = %v, want %v", err, failure)
		}
		wantAttachedHosts(t, failing, map[string]AttachedHostRecord{"alpha": oldRec})
		wantAttachedHosts(t, openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{}),
			map[string]AttachedHostRecord{"alpha": oldRec})
	})

	t.Run("after rename publishes new state", func(t *testing.T) {
		t.Parallel()
		fs := afero.NewMemMapFs()
		root := "/hub-state"
		failing := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{
			AfterRename: func() error { return failure },
		})
		if err := failing.Record("alpha", newRec); !errors.Is(err, failure) {
			t.Fatalf("Record error = %v, want %v", err, failure)
		}
		want := map[string]AttachedHostRecord{"alpha": newRec}
		wantAttachedHosts(t, failing, want)
		wantAttachedHosts(t, openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{}), want)
	})
}

func TestAttachedHostsStoreNilAndMemoryOnly(t *testing.T) {
	t.Parallel()
	var nilStore *AttachedHostsStore
	if got := nilStore.Snapshot(); len(got) != 0 {
		t.Fatalf("nil Snapshot() = %v, want empty", got)
	}
	if err := nilStore.Record("alpha", AttachedHostRecord{Generation: 1, IncarnationID: "inc"}); err != nil {
		t.Fatalf("nil Record error = %v, want nil", err)
	}
	if err := nilStore.Remove("alpha"); err != nil {
		t.Fatalf("nil Remove error = %v, want nil", err)
	}
	if err := nilStore.Prune(func(string, AttachedHostRecord) bool { return false }); err != nil {
		t.Fatalf("nil Prune error = %v, want nil", err)
	}

	store := openAttachedHostsStore(t, "")
	rec := AttachedHostRecord{Generation: 4, IncarnationID: "inc-mem"}
	if err := store.Record("alpha", rec); err != nil {
		t.Fatalf("memory-only Record: %v", err)
	}
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{"alpha": rec})
	if err := store.Remove("alpha"); err != nil {
		t.Fatalf("memory-only Remove: %v", err)
	}
	wantAttachedHosts(t, store, map[string]AttachedHostRecord{})
	if _, err := os.Stat(attachedHostsStatePath("")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("memory-only store touched disk at %q: %v", attachedHostsStatePath(""), err)
	}
}

func TestAttachedHostsStoreConcurrentAccess(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	root := "/hub-state"
	store := openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 20 {
				name := fmt.Sprintf("host-%d-%d", worker, i)
				if err := store.Record(name, AttachedHostRecord{Generation: uint64(i + 1), IncarnationID: name}); err != nil {
					errs <- err
					return
				}
				_ = store.Snapshot()
				if i%2 == 0 {
					if err := store.Remove(name); err != nil {
						errs <- err
						return
					}
				}
				if err := store.Prune(func(name string, _ AttachedHostRecord) bool { return len(name)%2 == 0 }); err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent store operation: %v", err)
	}
	wantAttachedHosts(t, openAttachedHostsStoreFS(t, fs, root, attachedHostsStoreFaults{}), store.Snapshot())
}
