package foldcache

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// intsFold is the test T: a running total plus a call count, so tests can
// distinguish "extend was called with the right fromOffset" from "extend was
// called at all."
type intsFold struct {
	sum   int
	lines int
}

// countingLineExtend returns an Extend[intsFold] that reads path as
// newline-delimited integers (one per line), summing new lines found at or
// after fromOffset onto prior, and records every (fromOffset) it was called
// with into calls (so tests can assert exactly which offsets were scanned).
func countingLineExtend(t *testing.T, calls *[]int64) Extend[intsFold] {
	t.Helper()
	var mu sync.Mutex
	return func(ctx context.Context, path string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		mu.Lock()
		*calls = append(*calls, fromOffset)
		mu.Unlock()
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return intsFold{}, 0, nil
			}
			return intsFold{}, 0, err
		}
		defer f.Close()
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return intsFold{}, 0, err
		}
		value := prior
		offset := fromOffset
		buf := make([]byte, 32*1024)
		var pending []byte
		for {
			if err := ctx.Err(); err != nil {
				return intsFold{}, 0, err
			}
			n, readErr := f.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				idx := indexByte(pending, '\n')
				if idx < 0 {
					break
				}
				line := pending[:idx]
				pending = pending[idx+1:]
				offset += int64(len(line)) + 1
				var v int
				for _, b := range line {
					v = v*10 + int(b-'0')
				}
				value.sum += v
				value.lines++
			}
			if readErr != nil {
				break
			}
		}
		return value, offset, nil
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func writeLines(t *testing.T, path string, lines []int) {
	t.Helper()
	var content strings.Builder
	for _, v := range lines {
		content.WriteString(itoa(v))
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines []int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, v := range lines {
		if _, err := f.WriteString(itoa(v) + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestCache_FirstGetReadsFromZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3})
	var calls []int64
	c := New[intsFold](8)

	result, err := c.Get(context.Background(), path, countingLineExtend(t, &calls))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if result.Value.sum != 6 || result.Value.lines != 3 {
		t.Fatalf("value = %+v, want sum=6 lines=3", result.Value)
	}
	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("calls = %v, want exactly one call with fromOffset=0", calls)
	}
}

// TestCache_SecondGetReadsOnlyTheAppendedDelta is the core incrementality
// proof (crux test b's shape, at the cache-package level): after an append,
// a second Get must call extend with fromOffset equal to what the first
// Get's Result.Offset reported — never 0 — so the underlying reader only
// ever sees bytes appended since the last successful read.
func TestCache_SecondGetReadsOnlyTheAppendedDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	// mtime resolution on some filesystems is coarse (1s); sleep to
	// guarantee the append moves mtime forward so the cache can't mistake
	// this for "nothing changed."
	time.Sleep(1100 * time.Millisecond)
	appendLines(t, path, []int{4, 5})

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 15 || second.Value.lines != 5 {
		t.Fatalf("second value = %+v, want sum=15 lines=5 (1+2+3+4+5)", second.Value)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want exactly 2 extend calls", calls)
	}
	if calls[1] != first.Offset {
		t.Fatalf("second call fromOffset = %d, want %d (first Get's reported offset) -- a full rescan from 0 is NOT incremental", calls[1], first.Offset)
	}
	if calls[1] == 0 {
		t.Fatalf("second call fromOffset = 0, want nonzero: the second Get must not re-read the file from the start")
	}
}

// TestCache_RepeatedGetWithNoChangeDoesNotCallExtend proves the "true hit"
// path: when the file's (size, mtime) are unchanged since the last Get, no
// extend call happens at all -- not even an incremental one over zero new
// bytes.
func TestCache_RepeatedGetWithNoChangeDoesNotCallExtend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	if _, err := c.Get(ctx, path, extend); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := c.Get(ctx, path, extend); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly 1 extend call across two Gets of an unchanged file", calls)
	}
}

// TestCache_ShrunkFileForcesFullRescanAndBumpsEpoch covers crux test (c):
// a rewritten/shrunk journal must force a full rescan (fromOffset=0) that
// produces the CORRECT result for the new content, and the epoch must bump
// so a continuation minted against the old content is detectably stale.
func TestCache_ShrunkFileForcesFullRescanAndBumpsEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3, 4, 5})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.Value.sum != 15 {
		t.Fatalf("first sum = %d, want 15", first.Value.sum)
	}

	time.Sleep(1100 * time.Millisecond)
	writeLines(t, path, []int{9}) // shrink + wholly different content

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 9 || second.Value.lines != 1 {
		t.Fatalf("second value = %+v, want sum=9 lines=1 -- the rescan must reflect the NEW content, not merge with the old", second.Value)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0 (a shrink must force a full rescan)", calls[len(calls)-1])
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch = %d, want it to differ from the first Get's %d after a shrink invalidated the cached fold", second.Epoch, first.Epoch)
	}
}

// TestCache_SameSizeDifferentMTimeRewriteForcesFullRescanAndBumpsEpoch is the
// jobstore.Store fileCursor's documented "residual" scenario applied to a
// read-only cache: a foreign rewrite that happens to land on the exact same
// byte count as before is still detected via mtime, forces a full rescan,
// and bumps epoch.
func TestCache_SameSizeDifferentMTimeRewriteForcesFullRescanAndBumpsEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2}) // "1\n2\n" == 4 bytes
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	writeLines(t, path, []int{3, 4}) // "3\n4\n" -- same 4 bytes, different content

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 7 {
		t.Fatalf("second sum = %d, want 7 (3+4) -- must reflect the rewritten content, not the stale cached '1+2'", second.Value.sum)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0", calls[len(calls)-1])
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch unchanged (%d) across a same-size rewrite, want it bumped", first.Epoch)
	}
}

// TestCache_GrowthWithUnchangedMTimeReadsFromZeroWithoutBumpingEpoch covers
// the mtime-can't-resolve-growth case jobstore.Store's fileCursor documents:
// on a coarse-mtime filesystem, a real append can leave mtime looking
// unchanged. The cache cannot trust an incremental extend in that case (it
// might double-count bytes it already has), so it must fall back to a full
// reread -- but since this is only ambiguous, not a proven rewrite, a
// client's already-issued continuation is still safe (append-only order is
// unaffected), so epoch must NOT bump.
func TestCache_GrowthWithUnchangedMTimeReadsFromZeroWithoutBumpingEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	frozenMTime := info.ModTime()
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	appendLines(t, path, []int{3})
	// Force mtime back to what it was before the append, simulating a
	// filesystem whose write-time granularity is too coarse to show it moved.
	if err := os.Chtimes(path, frozenMTime, frozenMTime); err != nil {
		t.Fatal(err)
	}

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 6 || second.Value.lines != 3 {
		t.Fatalf("second value = %+v, want sum=6 lines=3 (1+2+3) -- a full reread must still recover all the data", second.Value)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0 (growth without a resolvable mtime change must fall back to a full reread)", calls[len(calls)-1])
	}
	if second.Epoch != first.Epoch {
		t.Fatalf("epoch changed (%d -> %d) on an ambiguous-but-plausibly-safe append, want it unchanged since a continuation minted against the old data is still valid", first.Epoch, second.Epoch)
	}
}

// TestCache_ConcurrentGetsCoalesceToOneExtendCall is crux test (f)'s shape at
// the cache-package level: N concurrent Get calls for the same path must
// share a single extend call, not N redundant ones. Run with -race.
func TestCache_ConcurrentGetsCoalesceToOneExtendCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3})
	var extendCalls atomic.Int64
	release := make(chan struct{})
	extend := func(ctx context.Context, path string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		extendCalls.Add(1)
		<-release
		return intsFold{sum: 6, lines: 3}, 4 /* arbitrary */, nil
	}
	c := New[intsFold](8)
	const n = 16
	var wg sync.WaitGroup
	results := make([]Result[intsFold], n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.Get(context.Background(), path, extend)
		}(i)
	}
	// Wait until all n Get calls have registered (become the owner or
	// joined as a coalesced waiter) before releasing extend — a single
	// "the owner entered extend" signal isn't enough: the OTHER n-1
	// goroutines might not have reached their own registration yet, and if
	// extend returns before they do, they see a plain cache hit instead of
	// coalescing, making the test's own Coalesced assertion racy rather
	// than the implementation. Registration (Get's flights-map check,
	// under c.mu) is a fast, syscall-free critical section, so polling
	// Stats() is a tight loop, not a real wait, once the scheduler has
	// actually run all n goroutines.
	deadline := time.Now().Add(10 * time.Second)
	for {
		stats := c.Stats()
		if stats.Misses+stats.Coalesced >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for all %d Get calls to register (stats=%+v)", n, stats)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Get[%d]: %v", i, err)
		}
		if results[i].Value.sum != 6 {
			t.Fatalf("Get[%d].Value.sum = %d, want 6", i, results[i].Value.sum)
		}
	}
	if got := extendCalls.Load(); got != 1 {
		t.Fatalf("extend called %d times for %d concurrent Get calls on the same path, want exactly 1", got, n)
	}
	stats := c.Stats()
	if stats.Coalesced != n-1 {
		t.Fatalf("Stats().Coalesced = %d, want %d (one owner, the rest coalesced)", stats.Coalesced, n-1)
	}
}

// TestCache_ExtendErrorIsNotCached ensures a failed extend does not poison
// the cache: a later, successful Get for the same path must retry from
// scratch rather than replaying the error or a partial result forever.
func TestCache_ExtendErrorIsNotCached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	sentinel := errors.New("boom")
	attempt := 0
	extend := func(ctx context.Context, path string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		attempt++
		if attempt == 1 {
			return intsFold{}, 0, sentinel
		}
		return intsFold{sum: 3, lines: 2}, 4, nil
	}
	c := New[intsFold](8)
	ctx := context.Background()

	if _, err := c.Get(ctx, path, extend); !errors.Is(err, sentinel) {
		t.Fatalf("first Get error = %v, want sentinel", err)
	}
	result, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if result.Value.sum != 3 {
		t.Fatalf("second Get value = %+v, want sum=3", result.Value)
	}
}

// TestCache_EvictsLeastRecentlyUsedBeyondBound proves the LRU bound is real:
// with a max of 2 entries, touching a third path must evict the least
// recently used one, forcing its next Get to be a fresh (fromOffset=0) read.
func TestCache_EvictsLeastRecentlyUsedBeyondBound(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	pathC := filepath.Join(dir, "c.txt")
	writeLines(t, pathA, []int{1})
	writeLines(t, pathB, []int{2})
	writeLines(t, pathC, []int{3})
	var calls []int64
	c := New[intsFold](2)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	mustGet := func(path string) {
		t.Helper()
		if _, err := c.Get(ctx, path, extend); err != nil {
			t.Fatalf("Get(%s): %v", path, err)
		}
	}
	mustGet(pathA)
	mustGet(pathB)
	mustGet(pathC) // evicts A (least recently used)
	calls = nil
	mustGet(pathA)
	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("Get(A) after eviction: calls = %v, want one call with fromOffset=0", calls)
	}
	stats := c.Stats()
	if stats.Entries > 2 {
		t.Fatalf("Stats().Entries = %d, want at most the configured bound of 2", stats.Entries)
	}
	if stats.Evictions == 0 {
		t.Fatalf("Stats().Evictions = 0, want at least 1")
	}
}

// TestCache_MissingFileReadsAsZeroValueWithoutError mirrors ScanEvents'
// existing missing-file contract (no error, empty result) so callers that
// already handle "session has no journal yet" the same way keep working.
func TestCache_MissingFileReadsAsZeroValueWithoutError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	c := New[intsFold](8)
	extend := func(ctx context.Context, path string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		t.Fatalf("extend called for a nonexistent file")
		return intsFold{}, 0, nil
	}
	result, err := c.Get(context.Background(), path, extend)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if result.Value.sum != 0 || result.Value.lines != 0 {
		t.Fatalf("result.Value = %+v, want the zero value", result.Value)
	}
}

// TestCache_EvictionDoesNotHideAnInterveningRewriteFromEpoch covers the
// adversarial coherence review's CRITICAL finding: eviction used to reset a
// path's epoch counter to 0 (indistinguishable from "never seen"), so a
// continuation minted while a path's epoch was 0 -- the common case, since
// genuine rewrites are rare -- would pass its staleness check after ANY
// eviction, even one that raced a genuine rewrite. Epoch bookkeeping must
// survive eviction of the (potentially large) cached value.
func TestCache_EvictionDoesNotHideAnInterveningRewriteFromEpoch(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	writeLines(t, pathA, []int{1, 2, 3})
	writeLines(t, pathB, []int{9})
	var calls []int64
	c := New[intsFold](1) // bound of 1: touching B evicts A
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, pathA, extend)
	if err != nil {
		t.Fatalf("first Get(A): %v", err)
	}

	if _, err := c.Get(ctx, pathB, extend); err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if stats := c.Stats(); stats.Evictions == 0 {
		t.Fatalf("Stats().Evictions = 0, want pathA evicted by touching pathB under a 1-entry bound")
	}

	time.Sleep(1100 * time.Millisecond)
	writeLines(t, pathA, []int{9}) // shrink + wholly different content, WHILE evicted

	second, err := c.Get(ctx, pathA, extend)
	if err != nil {
		t.Fatalf("second Get(A): %v", err)
	}
	if second.Value.sum != 9 || second.Value.lines != 1 {
		t.Fatalf("second value = %+v, want sum=9 lines=1 (the rewrite, not stale merged content)", second.Value)
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch unchanged (%d) across an eviction that raced a genuine rewrite -- a continuation minted at the first Get's epoch would silently pass its staleness check against completely different content", first.Epoch)
	}
}

// TestCache_CanceledOwnerDoesNotPoisonAHealthyWaiter covers the adversarial
// coherence review's MAJOR finding: Get's singleflight coalescing used to
// deliver the flight OWNER's context-cancellation error to every coalesced
// WAITER, even a waiter whose own context was live and healthy. The owner's
// own cancellation must still surface to the owner; a coalesced waiter with
// its own healthy context must get the real result.
func TestCache_CanceledOwnerDoesNotPoisonAHealthyWaiter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2, 3})
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	extend := func(ctx context.Context, path string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		close(entered)
		<-release
		if err := ctx.Err(); err != nil {
			return intsFold{}, 0, err
		}
		return intsFold{sum: 6, lines: 3}, 4, nil
	}
	c := New[intsFold](8)

	var ownerErr error
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, ownerErr = c.Get(ownerCtx, path, extend)
	}()
	<-entered // owner has registered and is blocked inside extend

	waiterCtx := context.Background() // healthy: never touched
	var waiterResult Result[intsFold]
	var waiterErr error
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		waiterResult, waiterErr = c.Get(waiterCtx, path, extend)
	}()
	// Wait for the waiter to actually coalesce onto the owner's flight
	// before canceling anything, so this test proves the coalesced case,
	// not a race where the waiter happened to start its own separate
	// flight.
	deadline := time.Now().Add(10 * time.Second)
	for c.Stats().Coalesced < 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the waiter to coalesce onto the owner's flight")
		}
		time.Sleep(time.Millisecond)
	}

	cancelOwner()
	close(release)
	<-ownerDone
	<-waiterDone

	if !errors.Is(ownerErr, context.Canceled) {
		t.Fatalf("owner err = %v, want context.Canceled (sanity check: the owner's OWN cancellation must still surface to the owner)", ownerErr)
	}
	if waiterErr != nil {
		t.Fatalf("waiter err = %v, want nil -- the waiter's own context was never canceled, so the owner's cancellation must not poison it", waiterErr)
	}
	if waiterResult.Value.sum != 6 {
		t.Fatalf("waiter result = %+v, want the real sum=6 the (detached) extend call actually produced", waiterResult.Value)
	}
}

// TestCache_SameSizeSameMTimeRewriteIsDetectedViaTailProbe covers the
// adversarial coherence review's MAJOR finding: a same-size, same-mtime
// rewrite used to be an undetectable silent stale hit -- Get's own
// early-return "true hit" path never even reached refresh's staleness
// switch. A cheap tail probe closes this: the bytes at the cached offset
// must still match what was last observed before a hit is trusted.
func TestCache_SameSizeSameMTimeRewriteIsDetectedViaTailProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2}) // "1\n2\n" == 4 bytes
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	frozenMTime := info.ModTime()
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	writeLines(t, path, []int{3, 4}) // same 4 bytes, different content
	if err := os.Chtimes(path, frozenMTime, frozenMTime); err != nil {
		t.Fatal(err)
	}

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 7 {
		t.Fatalf("second sum = %d, want 7 (3+4) -- a same-size, same-mtime rewrite must not be served as a stale hit", second.Value.sum)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0 (a same-size same-mtime rewrite must force a full rescan)", calls[len(calls)-1])
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch unchanged (%d) across a same-size same-mtime rewrite the tail probe should have caught", first.Epoch)
	}
}

// TestCache_GrowingRewriteWithUnchangedMTimeBumpsEpoch covers the
// adversarial coherence review's MAJOR finding: growth with an
// unresolvable (unchanged) mtime was unconditionally treated as a safe
// append and never bumped epoch, even when the growth is actually a
// truncate-and-rewrite-larger that happens to coincide with the old mtime
// bucket. Unlike TestCache_GrowthWithUnchangedMTimeReadsFromZeroWithoutBumpingEpoch
// (a genuine append, where epoch correctly stays put), this constructs the
// untested sibling: a rewrite whose old prefix does NOT survive.
func TestCache_GrowingRewriteWithUnchangedMTimeBumpsEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2}) // "1\n2\n" == 4 bytes
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	frozenMTime := info.ModTime()
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	// Truncate and replace with different, LARGER content -- not an
	// append: the old prefix does not survive.
	writeLines(t, path, []int{30, 40, 50})
	if err := os.Chtimes(path, frozenMTime, frozenMTime); err != nil {
		t.Fatal(err)
	}

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 120 || second.Value.lines != 3 {
		t.Fatalf("second value = %+v, want sum=120 lines=3 (30+40+50) -- the full reread itself must still recover the new content", second.Value)
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch unchanged (%d) across a growing rewrite whose old prefix did NOT survive -- a continuation minted against the old 4-byte content would wrongly pass its staleness check against this entirely different content", first.Epoch)
	}
}
