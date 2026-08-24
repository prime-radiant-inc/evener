package hubcore

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestCovNewResumeLocks covers NewResumeLocks (resumelocks.go:16) and For
// (resumelocks.go:23). For must return the same mutex for the same id and
// distinct mutexes for different ids.
func TestCovNewResumeLocks(t *testing.T) {
	r := NewResumeLocks()
	if r == nil {
		t.Fatal("NewResumeLocks returned nil")
	}
	if r.locks == nil {
		t.Fatal("NewResumeLocks locks map is nil")
	}

	// First call creates a mutex for "s1".
	m1 := r.For("s1")
	if m1 == nil {
		t.Fatal("For(s1) returned nil")
	}

	// Second call with same id returns the same mutex.
	m1again := r.For("s1")
	if m1again != m1 {
		t.Fatal("For(s1) returned a different mutex on second call")
	}

	// Different id returns a different mutex.
	m2 := r.For("s2")
	if m2 == m1 {
		t.Fatal("For(s2) returned the same mutex as For(s1)")
	}

	// The returned mutex must be lockable.
	m1.Lock()
	m1.Unlock()
}

// TestCovResumeLocksConcurrent verifies For is safe under concurrent access.
func TestCovResumeLocksConcurrent(t *testing.T) {
	r := NewResumeLocks()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.For("shared")
		}()
	}
	wg.Wait()
	if r.For("shared") == nil {
		t.Fatal("For(shared) returned nil after concurrent access")
	}
}

// TestCovSessionMetaPath covers sessionMetaPath (past.go:223), a pure
// path-join helper.
func TestCovSessionMetaPath(t *testing.T) {
	got := sessionMetaPath("/projects/myproject", "01ABCDEF")
	want := filepath.Join("/projects/myproject", "sessions", "01ABCDEF.meta.json")
	if got != want {
		t.Errorf("sessionMetaPath = %q, want %q", got, want)
	}
}

// TestCovNewDeletionStore covers NewDeletionStore (deletion_store.go:72),
// which opens the host deletion store under stateRoot. With a fresh temp
// directory (no existing snapshot), it returns an empty store.
func TestCovNewDeletionStore(t *testing.T) {
	store, err := NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	if store == nil {
		t.Fatal("NewDeletionStore returned nil store")
	}
	// An empty store has no in-progress deletions.
	if records := store.Deleting(); records != nil {
		t.Fatalf("empty store Deleting() = %v, want nil", records)
	}
}
