package transcriptindex

import (
	"sync"
	"testing"
)

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
