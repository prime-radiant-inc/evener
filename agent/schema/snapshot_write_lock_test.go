package schema

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

// sessionMetaWriteLock's contract, stated as the invariant it exists to keep:
// a session-meta write is a read-modify-write of one session's persisted
// ObservedBy set followed by a non-atomic temp-file write and rename, all
// against paths keyed by the session ID. Concurrent writers for the SAME
// session ID must therefore exclude each other; writers for DIFFERENT session
// IDs touch disjoint files and must not.

// These three inspect package-global lock state with TryLock, so they must not
// run in parallel with anything else in the package that takes a meta write
// lock: another test holding the stripe under inspection would decide the
// result. Go resumes t.Parallel tests only once the sequential ones finish, so
// staying sequential is the isolation.

func TestSessionMetaWriteLockExcludesSameSession(t *testing.T) {
	const id = "02wMz5TxvEMoJEDTDGOTil"
	lock := sessionMetaWriteLock(id)
	lock.Lock()
	defer lock.Unlock()
	second := sessionMetaWriteLock(id)
	if second.TryLock() {
		second.Unlock()
		t.Fatalf("two writers for session %s acquired the meta write lock at once", id)
	}
}

func TestSessionMetaWriteLockIsolatesDistinctSessions(t *testing.T) {
	const (
		first  = "02wMz5TxvEMoJEDTDGOTil"
		second = "02wMz5TxvCu3kdckfnw0Gh"
	)
	// Two unrelated IDs can always land on one stripe by chance, and that is a
	// fixture problem, not the failure this test is looking for. Say which.
	if sessionMetaWriteLock(first) == sessionMetaWriteLock(second) {
		t.Fatalf("fixture: session IDs %s and %s hash to the same stripe; pick a different pair", first, second)
	}
	lock := sessionMetaWriteLock(first)
	lock.Lock()
	defer lock.Unlock()
	other := sessionMetaWriteLock(second)
	if !other.TryLock() {
		t.Fatalf("a write for session %s blocked behind an unrelated write for %s", second, first)
	}
	other.Unlock()
}

// TestSessionMetaWriteLockSharesLockAcrossAliasingIDs pins the shard key to a
// case-folded session ID: two IDs that name one meta file must share a lock even
// when they are not the same string, and ValidateSessionID admits IDs that
// differ only in case. Path-shaped aliases are no longer this function's problem
// — ValidateSessionID refuses separators and "." outright, so none can reach it.
func TestSessionMetaWriteLockSharesLockAcrossAliasingIDs(t *testing.T) {
	for _, aliases := range [][2]string{
		{"02wMz5TxvEMoJEDTDGOTil", "02wMz5TxvEMoJEDTDGOTIL"},
		{"WORKER", "worker"},
	} {
		if sessionMetaWriteLock(aliases[0]) != sessionMetaWriteLock(aliases[1]) {
			t.Errorf("session IDs %q and %q name one meta file but mapped to different write locks", aliases[0], aliases[1])
		}
	}
}

// TestConcurrentSaveSessionMetaUnionsObservedBy is the black-box guard on the
// same-session invariant: every writer contributes one observer, and the
// read-modify-write union must lose none of them. Sharding more finely than
// per-session ID (or dropping the lock) drops observers here.
func TestConcurrentSaveSessionMetaUnionsObservedBy(t *testing.T) {
	t.Parallel()
	const (
		worker  = "02wMz5TxvEMoJEDTDGOTil"
		writers = 16
	)
	dir := t.TempDir()
	fs := afero.NewOsFs()
	if err := SaveSessionMetaWithFS(fs, dir, SessionMeta{ID: worker}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			meta := SessionMeta{ID: worker, ObservedBy: []string{fmt.Sprintf("observer_%02d", i)}}
			if err := SaveSessionMetaWithFS(fs, dir, meta); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}

	got, err := LoadSessionMetaWithFS(fs, dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(got.ObservedBy))
	for _, observer := range got.ObservedBy {
		seen[observer] = true
	}
	for i := range writers {
		want := fmt.Sprintf("observer_%02d", i)
		if !seen[want] {
			t.Fatalf("observer %s lost by a concurrent save; ObservedBy = %q", want, got.ObservedBy)
		}
	}
}

// TestConcurrentAppendSessionObservedByUnionsAll covers the other writer of the
// same read-modify-write: the observer-append path.
func TestConcurrentAppendSessionObservedByUnionsAll(t *testing.T) {
	t.Parallel()
	const (
		worker    = "02wMz5TxvEMoJEDTDGOTil"
		observers = 16
	)
	dir := t.TempDir()
	fs := afero.NewOsFs()
	if err := SaveSessionMetaWithFS(fs, dir, SessionMeta{ID: worker}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, observers)
	for i := range observers {
		wg.Go(func() {
			if err := appendSessionObservedByWithFS(fs, dir, worker, fmt.Sprintf("observer_%02d", i)); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent observer append: %v", err)
	}

	got, err := LoadSessionMetaWithFS(fs, dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ObservedBy) != observers {
		t.Fatalf("ObservedBy = %q, want %d distinct observers", got.ObservedBy, observers)
	}
}

// TestConcurrentSaveSessionMetaDistinctSessions proves the sharded lock still
// leaves every unrelated session's file intact when many are written at once.
func TestConcurrentSaveSessionMetaDistinctSessions(t *testing.T) {
	t.Parallel()
	ids := []string{
		"02wMz5TxvEMoJEDTDGOTil",
		"02wMz5TxvCu3kdckfnw0Gh",
		"02wMz5TxvCu3kdckfnw0Gi",
		"02wMz5TxvCu3kdckfnw0Gj",
		"02wMz5TxvCu3kdckfnw0Gk",
		"02wMz5TxvCu3kdckfnw0Gl",
		"02wMz5TxvCu3kdckfnw0Gm",
		"02wMz5TxvCu3kdckfnw0Gn",
	}
	dir := t.TempDir()
	fs := afero.NewOsFs()

	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Go(func() {
			meta := SessionMeta{ID: id, Name: "session " + id, ObservedBy: []string{"observer_" + id}}
			if err := SaveSessionMetaWithFS(fs, dir, meta); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}

	for _, id := range ids {
		got, err := LoadSessionMetaWithFS(fs, dir, id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if got.Name != "session "+id || len(got.ObservedBy) != 1 || got.ObservedBy[0] != "observer_"+id {
			t.Fatalf("meta for %s crossed with another session: %+v", id, got)
		}
	}
}

// BenchmarkSaveSessionMetaContention measures the cost of a session-meta write
// when many distinct sessions save at once — the production shape the write
// lock's granularity governs. It deliberately uses the OS filesystem: an
// afero.MemMapFs serializes every operation behind its own filesystem-wide
// mutex and would hide the write lock's contribution entirely.
func BenchmarkSaveSessionMetaContention(b *testing.B) {
	dir := b.TempDir()
	fs := afero.NewOsFs()
	var next atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := fmt.Sprintf("02wMz5TxvEMoJEDTDG%04d", next.Add(1))
		meta := SessionMeta{ID: id, Name: "bench"}
		for pb.Next() {
			if err := SaveSessionMetaWithFS(fs, dir, meta); err != nil {
				b.Fatal(err)
			}
		}
	})
}
