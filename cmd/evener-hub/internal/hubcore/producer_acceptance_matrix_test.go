package hubcore

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestProducerAcceptanceRosterNoopAndOneChange(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 71, Address: "127.0.0.1:71"})
	r := NewRoster(dir, fakeProber{sessionID: "01ROSTER", status: "idle"})
	var calls atomic.Int32
	r.SetOnChange(func() {
		calls.Add(1)
		_ = r.List() // callback must be outside the roster lock.
	})
	r.Refresh()
	if got := calls.Load(); got != 1 {
		t.Fatalf("initial roster publish callbacks = %d, want 1", got)
	}
	r.Refresh()
	if got := calls.Load(); got != 1 {
		t.Fatalf("roster no-op callbacks = %d, want 1", got)
	}
	if err := rendezvous.Remove(dir, 71); err != nil {
		t.Fatal(err)
	}
	r.Refresh()
	if got := calls.Load(); got != 2 {
		t.Fatalf("roster removal callbacks = %d, want 2", got)
	}
}

func TestProducerAcceptancePastNoopAndOneChange(t *testing.T) {
	root := t.TempDir()
	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	var calls atomic.Int32
	idx.SetOnChange(func() {
		calls.Add(1)
		_ = idx.All() // callback must be outside the index lock.
	})
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("initial past publish callbacks = %d, want 1", got)
	}
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("past no-op callbacks = %d, want 1", got)
	}
	project := filepath.Join(root, "projects", "project-a-0123456789")
	writeMeta(t, project, schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "one"})
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("past change callbacks = %d, want 2", got)
	}
}

func TestProducerAcceptanceRemoteNormalizesRetainedFailureAndNoop(t *testing.T) {
	cache := &RemoteThreadCache{}
	var calls atomic.Int32
	cache.SetOnChange(func() {
		calls.Add(1)
		_ = cache.Snapshot() // callback must be outside the cache lock.
	})
	cache.StoreSnapshotData(RemoteThreadSnapshot{Complete: true})
	if got := calls.Load(); got != 1 {
		t.Fatalf("initial remote publish callbacks = %d, want 1", got)
	}
	generation := cache.Snapshot().Generation
	cache.StoreSnapshotData(RemoteThreadSnapshot{Threads: nil, Sources: nil, Complete: true})
	if got := calls.Load(); got != 1 || cache.Snapshot().Generation != generation {
		t.Fatalf("remote normalized no-op callbacks=%d generation=%d want 1/%d", got, calls.Load(), generation)
	}
	cache.StoreSnapshotData(RemoteThreadSnapshot{Complete: false}) // retained failure is a real completeness change.
	if got := calls.Load(); got != 2 {
		t.Fatalf("remote retained-failure callbacks = %d, want 2", got)
	}
}

func TestProducerAcceptancePinOneCallbackPerChangedPath(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	var calls atomic.Int32
	store.SetOnChange(func() {
		calls.Add(1)
		_, _ = store.Sections() // callback must be outside the transaction/DB lock.
	})
	now := time.Unix(100, 0)
	section, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", now)
	if err != nil || !changed || calls.Load() != 1 {
		t.Fatalf("create changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.CreateOrReuseAndAssign("research", "session-a", now.Add(time.Second)); err != nil || changed || calls.Load() != 1 {
		t.Fatalf("create/reuse no-op changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.Rename(section.ID, "Research", now.Add(2*time.Second)); err != nil || changed || calls.Load() != 1 {
		t.Fatalf("rename no-op changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.Rename(section.ID, "Renamed", now.Add(3*time.Second)); err != nil || !changed || calls.Load() != 2 {
		t.Fatalf("rename changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.Assign(section.ID, "session-a", now.Add(4*time.Second)); err != nil || changed || calls.Load() != 2 {
		t.Fatalf("assign no-op changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.Assign(section.ID, "session-b", now.Add(5*time.Second)); err != nil || !changed || calls.Load() != 3 {
		t.Fatalf("assign changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if changed, err = store.Unpin("missing"); err != nil || changed || calls.Load() != 3 {
		t.Fatalf("unpin absent changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if changed, err = store.Unpin("session-b"); err != nil || !changed || calls.Load() != 4 {
		t.Fatalf("unpin changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
	if _, changed, err = store.DeleteSection(section.ID); err != nil || !changed || calls.Load() != 5 {
		t.Fatalf("delete changed=%v callbacks=%d err=%v", changed, calls.Load(), err)
	}
}

func TestProducerAcceptanceArchiveHintsAfterSuccessfulWrites(t *testing.T) {
	store := NewArchiveStore(filepath.Join(t.TempDir(), "index.db"))
	var calls atomic.Int32
	store.SetOnChange(func() {
		calls.Add(1)
		_, _ = store.Decisions() // callback must run after the write transaction.
	})
	now := time.Unix(100, 0)
	operations := []func() error{
		func() error { return store.Set("session", "archive-a", true, now) },
		func() error { return store.Set("session", "archive-a", true, now.Add(time.Second)) },
		func() error { return store.Delete("session", "missing") },
		func() error { return store.Delete("session", "archive-a") },
	}
	for i, operation := range operations {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		if got, want := calls.Load(), int32(i+1); got != want {
			t.Fatalf("archive successful-write hints = %d, want %d", got, want)
		}
	}
}

func TestProducerAcceptanceFavoriteHintsAfterSuccessfulWrites(t *testing.T) {
	store := NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	var calls atomic.Int32
	store.SetOnChange(func() {
		calls.Add(1)
		_, _ = store.Favorites() // callback must run after the write transaction.
	})
	now := time.Unix(100, 0)
	operations := []func() error{
		func() error { return store.Set("session", "favorite-a", true, now) },
		func() error { return store.Set("session", "favorite-a", true, now.Add(time.Second)) },
		func() error { return store.Delete("session", "missing") },
		func() error { return store.Delete("session", "favorite-a") },
	}
	for i, operation := range operations {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		if got, want := calls.Load(), int32(i+1); got != want {
			t.Fatalf("favorite successful-write hints = %d, want %d", got, want)
		}
	}
}

func TestProducerAcceptanceRemoteSourceMapChangeIsFingerprinted(t *testing.T) {
	cache := &RemoteThreadCache{}
	thread := appwire.Thread{ID: "remote-1", Source: "source-a"}
	cache.StoreSnapshotData(RemoteThreadSnapshot{Threads: []appwire.Thread{thread}, Complete: true, Sources: map[string]RemoteSourceSnapshot{
		"source-a": {Threads: []appwire.Thread{thread}, Complete: true},
	}})
	got := cache.Snapshot()
	if len(got.Sources["source-a"].Threads) != 1 || got.Sources["source-a"].Threads[0].ID != thread.ID {
		t.Fatalf("inferred remote source = %+v", got.Sources)
	}
}

func TestProducerAcceptanceRemoteAuthoritativeSourcesMatrix(t *testing.T) {
	cache := &RemoteThreadCache{}
	var calls atomic.Int32
	cache.SetOnChange(func() {
		calls.Add(1)
		_ = cache.Snapshot() // callback must be outside the cache lock.
	})
	thread := appwire.Thread{ID: "remote-1", Source: "source-a"}
	base := RemoteThreadSnapshot{
		Threads:  []appwire.Thread{thread},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"source-a": {Threads: []appwire.Thread{thread}, Complete: true},
		},
	}
	cache.StoreSnapshotData(base)
	firstGeneration := cache.Snapshot().Generation
	cache.StoreSnapshotData(RemoteThreadSnapshot{
		Threads:  []appwire.Thread{thread},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"source-a": {Threads: []appwire.Thread{thread}, Complete: true},
		},
	})
	if cache.Snapshot().Generation != firstGeneration || calls.Load() != 1 {
		t.Fatalf("deep-equivalent source map changed generation=%d callbacks=%d", cache.Snapshot().Generation, calls.Load())
	}
	changed := base
	changed.Sources = map[string]RemoteSourceSnapshot{
		"source-a": {Threads: []appwire.Thread{thread}, Complete: false, IncompleteIDs: []string{"remote-bad"}},
	}
	cache.StoreSnapshotData(changed)
	if cache.Snapshot().Generation != firstGeneration+1 || calls.Load() != 2 {
		t.Fatalf("source-map content change generation=%d callbacks=%d, want %d/2", cache.Snapshot().Generation, calls.Load(), firstGeneration+1)
	}
}
