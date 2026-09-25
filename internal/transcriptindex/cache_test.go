package transcriptindex

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refCount is the cache's bookkeeping for path, for tests that need to see
// past what a handle's own state reveals (Close/Release don't expose it).
func (c *Cache) refCount(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return 0
	}
	return e.refs
}

// openPaths returns count sidecar transcript paths under t's temp dir, each
// with its own header-only transcript so Open succeeds.
func openPaths(t testing.TB, count int) []string {
	t.Helper()
	paths := make([]string, count)
	for i := range paths {
		paths[i] = writeFixture(t, fixture{name: "cache", header: everything().header})
	}
	return paths
}

func isClosed(x *Index) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.lock == nil
}

func TestCacheEvictsTheLeastRecentlyReleasedHandle(t *testing.T) {
	paths := openPaths(t, 3)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Release(a)
	b, err := c.Acquire(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	c.Release(b)

	// Both idle handles fit under capacity 2; acquiring a third evicts a,
	// the least recently released.
	cx, err := c.Acquire(paths[2])
	if err != nil {
		t.Fatal(err)
	}
	if !isClosed(a) {
		t.Fatal("the least recently released handle was not evicted")
	}
	if isClosed(b) {
		t.Fatal("the more recently released handle was evicted instead")
	}
	c.Release(cx)
}

func TestCacheNeverClosesAnAcquiredHandle(t *testing.T) {
	paths := openPaths(t, 3)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Acquire(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	// Neither a nor b is released: acquiring a third must not evict either,
	// even though it puts the cache over capacity.
	cx, err := c.Acquire(paths[2])
	if err != nil {
		t.Fatal(err)
	}
	if isClosed(a) || isClosed(b) {
		t.Fatal("an acquired handle was closed")
	}
	c.Release(a)
	c.Release(b)
	c.Release(cx)
}

func TestCacheAcquireReturnsTheSameHandleWhileInUse(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if a != again {
		t.Fatal("Acquire opened a second handle for a path already held")
	}
	c.Release(a)
	c.Release(again)
}

func TestCacheForgetClosesAnUnusedHandle(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Release(a)
	c.Forget(paths[0])
	if !isClosed(a) {
		t.Fatal("Forget did not close the unused handle")
	}
}

func TestCacheForgetLeavesAnAcquiredHandleOpenUntilReleased(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Forget(paths[0])
	if isClosed(a) {
		t.Fatal("Forget closed a handle still in use")
	}
	c.Release(a)
}

func TestCacheAcquireAfterCloseErrors(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(paths[0]); err == nil {
		t.Fatal("Acquire after Close succeeded")
	}
}

// TestCacheCloseWithAHandleOutClosesItOnRelease requires Close to leave a
// still-acquired handle open and usable, closing it only once its last
// Release comes in; a further Acquire, of the same or another path, errors
// once Close has run.
func TestCacheCloseWithAHandleOutClosesItOnRelease(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if isClosed(a) {
		t.Fatal("Close closed a handle still acquired")
	}
	if _, err := a.Latest(1); err != nil {
		t.Fatalf("handle still held after Close is unusable: %v", err)
	}
	if _, err := c.Acquire(paths[0]); err == nil {
		t.Fatal("Acquire after Close succeeded")
	}

	c.Release(a)
	if !isClosed(a) {
		t.Fatal("releasing the last handle after Close did not close it")
	}
}

// TestCacheReleaseWithoutAMatchingAcquirePanics requires a double Release (or
// any Release of a handle the cache did not hand out) to panic loudly rather
// than corrupt the idle list.
func TestCacheReleaseWithoutAMatchingAcquirePanics(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	a, err := c.Acquire(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Release(a)

	defer func() {
		if recover() == nil {
			t.Fatal("a second Release of the same handle did not panic")
		}
	}()
	c.Release(a)
}

// TestCacheAcquireDoesNotBlockOtherPathsDuringOpen requires a cold miss's
// Open call to run outside the cache's lock: another path's Acquire and
// Release must complete while the miss is still inside Open.
func TestCacheAcquireDoesNotBlockOtherPathsDuringOpen(t *testing.T) {
	paths := openPaths(t, 2)
	c := NewCache(4)
	defer c.Close() //nolint:errcheck // test cleanup

	parked := make(chan struct{})
	release := make(chan struct{})
	old := cacheOpenHook
	cacheOpenHook = func(path string) {
		if path == paths[0] {
			close(parked)
			<-release
		}
	}
	defer func() { cacheOpenHook = old }()

	aDone := make(chan error, 1)
	go func() {
		x, err := c.Acquire(paths[0])
		if err == nil {
			c.Release(x)
		}
		aDone <- err
	}()
	<-parked // a's Acquire is now parked inside Open, holding no lock

	bResult := make(chan error, 1)
	go func() {
		x, err := c.Acquire(paths[1])
		if err == nil {
			c.Release(x)
		}
		bResult <- err
	}()
	select {
	case err := <-bResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Acquire of another path blocked while a miss was inside Open")
	}

	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
}

// TestCacheConcurrentAcquireOfOnePathOpensOnce requires two concurrent
// Acquires of the same path to share one Open call and both end up with the
// same handle, refcounted twice.
func TestCacheConcurrentAcquireOfOnePathOpensOnce(t *testing.T) {
	paths := openPaths(t, 1)
	c := NewCache(4)
	defer c.Close() //nolint:errcheck // test cleanup

	var opens atomic.Int32
	old := cacheOpenHook
	cacheOpenHook = func(string) { opens.Add(1) }
	defer func() { cacheOpenHook = old }()

	var wg sync.WaitGroup
	handles := make([]*Index, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			handles[i], errs[i] = c.Acquire(paths[0])
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if handles[0] != handles[1] {
		t.Fatal("concurrent Acquires of one path returned different handles")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("Open called %d times for one path acquired concurrently, want 1", got)
	}
	if got := c.refCount(paths[0]); got != 2 {
		t.Fatalf("refcount = %d after two Acquires, want 2", got)
	}
	c.Release(handles[0])
	if got := c.refCount(paths[0]); got != 1 {
		t.Fatalf("refcount = %d after one Release of two Acquires, want 1", got)
	}
	c.Release(handles[1])
}

func TestCacheConcurrentAcquireRelease(t *testing.T) {
	paths := openPaths(t, 6)
	c := NewCache(3)
	defer c.Close() //nolint:errcheck // test cleanup

	var wg sync.WaitGroup
	for i := range 20 {
		path := paths[i%len(paths)]
		wg.Go(func() {
			x, err := c.Acquire(path)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := x.Latest(1); err != nil {
				t.Error(err)
			}
			c.Release(x)
		})
	}
	wg.Wait()
}
