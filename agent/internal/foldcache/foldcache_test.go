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

// TestCache_EvictionDoesNotHideAnInterveningRewriteFromEpoch proves
// eviction must never reset a path's epoch counter to 0 (indistinguishable
// from "never seen"): a continuation minted while a path's epoch was 0 --
// the common case, since genuine rewrites are rare -- would then pass its
// staleness check after ANY eviction, even one that raced a genuine
// rewrite. Epoch bookkeeping must survive eviction of the (potentially
// large) cached value.
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

// TestCache_CanceledOwnerDoesNotPoisonAHealthyWaiter proves Get's
// singleflight coalescing must never deliver the flight OWNER's
// context-cancellation error to a coalesced WAITER whose own context is
// live and healthy. The owner's own cancellation must still surface to the
// owner; a coalesced waiter with its own healthy context must get the real
// result.
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

// TestCache_SameSizeSameMTimeRewriteIsDetectedViaTailProbe proves a
// same-size, same-mtime rewrite cannot be an undetectable silent stale
// hit: without a tail probe, Get's own early-return "true hit" path would
// never even reach refresh's staleness switch. A cheap tail probe closes
// this: the bytes at the cached offset must still match what was last
// observed before a hit is trusted.
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

// TestCache_GrowingRewriteWithUnchangedMTimeBumpsEpoch proves growth with
// an unresolvable (unchanged) mtime must never be treated unconditionally
// as a safe append with epoch left unbumped, even when the growth is
// actually a truncate-and-rewrite-larger that happens to coincide with the
// old mtime bucket. Unlike TestCache_GrowthWithUnchangedMTimeReadsFromZeroWithoutBumpingEpoch
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

// TestCache_GrowingRewriteWithChangedMTimeIsDetectedViaTailProbe proves
// growth where mtime DID change cannot be trusted as a safe append without
// verification: refresh's default case must not hand extend the OLD cached
// (prior, fromOffset) pair unverified, the same as its
// TestCache_GrowingRewriteWithUnchangedMTimeBumpsEpoch sibling (same-mtime
// growth), which already runs the tail probe. A truncate-and-replace with
// different, LARGER content and a genuinely later
// mtime is indistinguishable from a real append using (size, mtime) alone --
// "mtime moved forward" is exactly what a real writer's progress looks like
// too. This constructs exactly that case: without the probe, extend
// resumes from the stale offset and reads into the middle of the new
// file's unrelated bytes, welding a nonsense fold (neither the old value
// nor the new file's true content) instead of forcing a clean full rescan.
func TestCache_GrowingRewriteWithChangedMTimeIsDetectedViaTailProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2}) // "1\n2\n" == 4 bytes
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMTime := info.ModTime()
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	// Truncate and replace with different, LARGER content -- not an
	// append: the old prefix does not survive. Unlike the unchanged-mtime
	// sibling, give this a genuinely LATER mtime: the exact case
	// (size, mtime) alone cannot distinguish from a real append.
	writeLines(t, path, []int{30, 40, 50})
	laterMTime := firstMTime.Add(time.Second)
	if err := os.Chtimes(path, laterMTime, laterMTime); err != nil {
		t.Fatal(err)
	}

	second, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Value.sum != 120 || second.Value.lines != 3 {
		t.Fatalf("second value = %+v, want sum=120 lines=3 (30+40+50) -- an unverified resume from the stale offset welds the old fold onto a misaligned read of the new content instead of a clean full rescan", second.Value)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0 (a growing rewrite with a changed mtime must still be probed, not trusted outright, and a mismatch must force a full rescan)", calls[len(calls)-1])
	}
	if second.Epoch == first.Epoch {
		t.Fatalf("epoch unchanged (%d) across a growing rewrite with a different mtime whose old prefix did NOT survive -- a continuation minted against the old 4-byte content would wrongly pass its staleness check against this entirely different content", first.Epoch)
	}
}

// TestCache_DeletedFileEpochSurvivesAcrossRecreation proves dropping a
// deleted path's epochState outright must not happen: doing so would let a
// path that later reappears start back at epoch 0 -- indistinguishable
// from "never seen" -- silently accepting a continuation minted before the
// deletion against the unrelated new content that replaced it. drop must
// instead tombstone: keep the epoch (bumped past its pre-deletion value),
// discard every content signal (size/mod/offset/tail), so the recreated
// path is read completely fresh but its epoch still records that the file
// underneath the path changed identity.
func TestCache_DeletedFileEpochSurvivesAcrossRecreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)

	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get on deleted file: %v", err)
	}
	if missing.Value != (intsFold{}) {
		t.Fatalf("missing-file value = %+v, want the zero value", missing.Value)
	}

	// Recreate with entirely different content -- a new file that just
	// happens to reuse the same path.
	writeLines(t, path, []int{100, 200, 300})

	recreated, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get on recreated file: %v", err)
	}
	if recreated.Value.sum != 600 || recreated.Value.lines != 3 {
		t.Fatalf("recreated value = %+v, want sum=600 lines=3 (100+200+300)", recreated.Value)
	}
	if calls[len(calls)-1] != 0 {
		t.Fatalf("last call fromOffset = %d, want 0 (a recreated file must be read fresh, never resumed from the deleted file's offset)", calls[len(calls)-1])
	}
	if recreated.Epoch <= first.Epoch {
		t.Fatalf("recreated epoch (%d) did not advance past the pre-deletion epoch (%d) -- a continuation minted before the deletion would wrongly pass its staleness check against this entirely unrelated new content", recreated.Epoch, first.Epoch)
	}
}

// tornStatInfo reports a file's size as it was before an append and its mtime
// as it is after one — the observation os.Stat can return while a writer is
// appending, since the two fields are not read atomically.
type tornStatInfo struct {
	os.FileInfo
	size int64
	mod  time.Time
}

func (i tornStatInfo) Size() int64        { return i.size }
func (i tornStatInfo) ModTime() time.Time { return i.mod }

// TestCache_TornAppendStatKeepsTheGeneration pins the rule the same-size
// branch has to follow. os.Stat is not an atomic snapshot: during an append it
// can report the size from before the write and the mtime from after it, which
// looks exactly like a same-size rewrite. Bumping on that appearance alone
// discards a fold nothing invalidated and moves a generation the file's own
// content does not justify — and a continuation keyed to the old generation is
// then refused for a journal that was only appended to. The tail probe tells
// the two apart, so it decides here as it does in every other ambiguous case.
func TestCache_TornAppendStatKeepsTheGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	c.mu.Lock()
	st := c.epochStates[path]
	c.mu.Unlock()
	if st == nil {
		t.Fatal("no state recorded for a folded path")
	}

	appendLines(t, path, []int{3})
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	torn := tornStatInfo{FileInfo: current, size: st.size, mod: current.ModTime().Add(time.Second)}

	refreshed, err := c.refresh(ctx, path, torn, extend)
	if err != nil {
		t.Fatalf("refresh on a torn stat: %v", err)
	}
	if refreshed.Epoch != folded.Epoch {
		t.Fatalf("generation moved to %d on a torn append stat, want it to stay at %d -- the fold reads the whole append and keeps the old one, so this discards a fold nothing invalidated", refreshed.Epoch, folded.Epoch)
	}
	if refreshed.Value.sum != 6 {
		t.Fatalf("value = %+v, want the appended content read in full (1+2+3)", refreshed.Value)
	}
}

// TestCache_SameSizeRewriteKeepingItsTailBumpsTheGeneration pins the other
// half of the same-size rule. The tail probe alone cannot separate a torn
// append from a rewrite that happens to land on the same length AND leave the
// probed trailing bytes intact -- a journal rewritten at the same size with
// the same last record is exactly that. Keeping the generation there accepts
// an outstanding continuation whose ResumeIndex now points into content that
// changed underneath it. A second stat settles it: a torn append has finished
// by the time it is taken and reports the larger size, while a rewrite still
// reports the same one.
func TestCache_SameSizeRewriteKeepingItsTailBumpsTheGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	original := make([]int, 0, 40)
	for v := 10; v < 50; v++ {
		original = append(original, v)
	}
	writeLines(t, path, original)
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if folded.Value.sum != 1180 {
		t.Fatalf("first fold summed %d, want 1180", folded.Value.sum)
	}

	// Same line count and same line width, so the same total size; only the
	// leading lines change, which leaves the last 66 bytes -- more than the
	// 64 the probe reads -- byte-identical.
	rewritten := make([]int, len(original))
	copy(rewritten, original)
	for i := range 18 {
		rewritten[i] = 99
	}
	writeLines(t, path, rewritten)
	stamp := time.Unix(1_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	after, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after a same-size rewrite: %v", err)
	}
	if after.Epoch != folded.Epoch+1 {
		t.Fatalf("generation = %d after a same-size rewrite that kept its trailing bytes, want %d -- the fold this cache held is gone, so a continuation keyed to it must not be accepted", after.Epoch, folded.Epoch+1)
	}
	if after.Value.sum != 2629 {
		t.Fatalf("value = %+v, want the rewritten content read in full (sum 2629)", after.Value)
	}
}

// TestCache_TornStatAppendRecordsTheSizeItActuallyFolded pins what a torn
// stat leaves behind. The fold reads the whole appended file, so the state
// this cache publishes has to describe the content it consumed -- recording
// the pre-append size the torn stat reported leaves offset ahead of size,
// which no honest file can produce. The next stat then reads as growth, the
// growth path trusts the surviving tail and resumes from an offset the file
// has already reached, and a same-size rewrite underneath is served from the
// stale fold with its generation intact.
func TestCache_TornStatAppendRecordsTheSizeItActuallyFolded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	original := make([]int, 0, 40)
	for v := 10; v < 50; v++ {
		original = append(original, v)
	}
	writeLines(t, path, original)
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	c.mu.Lock()
	st := c.epochStates[path]
	c.mu.Unlock()
	if st == nil {
		t.Fatal("no state recorded for a folded path")
	}

	appended := make([]int, 0, 10)
	for v := 50; v < 60; v++ {
		appended = append(appended, v)
	}
	appendLines(t, path, appended)
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	torn := tornStatInfo{FileInfo: current, size: st.size, mod: current.ModTime().Add(time.Second)}
	tornResult, err := c.refresh(ctx, path, torn, extend)
	if err != nil {
		t.Fatalf("refresh on a torn stat: %v", err)
	}
	if tornResult.Epoch != folded.Epoch {
		t.Fatalf("generation moved to %d on a torn append stat, want it to stay at %d", tornResult.Epoch, folded.Epoch)
	}
	if tornResult.Value.sum != 1725 {
		t.Fatalf("torn-stat fold = %+v, want the whole appended file (sum 1725)", tornResult.Value)
	}

	// A rewrite at the size the file really has, changing only lines before
	// the probed window. The recorded state has to see this as a rewrite;
	// if it still believes the pre-append size, it sees growth instead,
	// resumes from an offset the file already reached, and reads nothing.
	rewritten := make([]int, 0, 50)
	for i := range 50 {
		if i < 28 {
			rewritten = append(rewritten, 99)
			continue
		}
		rewritten = append(rewritten, 10+i)
	}
	writeLines(t, path, rewritten)
	stamp := time.Unix(1_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	after, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after the rewrite: %v", err)
	}
	if after.Value.sum != 3839 {
		t.Fatalf("value = %+v, want the rewritten content read in full (sum 3839) -- a fold resumed past the end of the new file reads none of it", after.Value)
	}
	if after.Epoch != tornResult.Epoch+1 {
		t.Fatalf("generation = %d after a same-size rewrite following a torn stat, want %d", after.Epoch, tornResult.Epoch+1)
	}
}

// TestCache_RewriteInsideTheSecondStatWindowBumpsTheGeneration closes the
// window the second stat opens. The probe runs first and the stat after it,
// so a rewrite that also appends can land in between: the stat then reports a
// file longer than the recorded size, which reads as the completed append
// that a torn first stat implies, while the prefix the probe just vouched for
// is gone. Growth alone is not evidence the fold survived, so the probe is
// asked again once the length is known.
func TestCache_RewriteInsideTheSecondStatWindowBumpsTheGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	original := make([]int, 0, 40)
	for v := 10; v < 50; v++ {
		original = append(original, v)
	}
	writeLines(t, path, original)
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	c.mu.Lock()
	st := c.epochStates[path]
	c.mu.Unlock()
	if st == nil {
		t.Fatal("no state recorded for a folded path")
	}

	// Replace the file wholesale, longer than it was, at the one moment
	// that lies between the probe and the stat.
	replacement := make([]int, 50)
	for i := range replacement {
		replacement[i] = 99
	}
	stats := 0
	c.stat = func(p string) (os.FileInfo, error) {
		stats++
		// Only the first: that is the stat settling the same-size
		// ambiguity, the one whose window this test is about. The second
		// is the post-fold stat that records the mtime of the content the
		// fold just read, and rewriting under that one would describe a
		// file this fold never saw.
		if stats == 1 {
			writeLines(t, p, replacement)
		}
		return os.Stat(p)
	}

	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	torn := tornStatInfo{FileInfo: current, size: st.size, mod: current.ModTime().Add(time.Second)}
	after, err := c.refresh(ctx, path, torn, extend)
	if err != nil {
		t.Fatalf("refresh across a rewrite in the stat window: %v", err)
	}
	if stats != 2 {
		t.Fatalf("refresh stat'd %d times, want 2: the one that settles the same-size ambiguity, then the one that records the mtime of what the fold read", stats)
	}
	if after.Epoch != folded.Epoch+1 {
		t.Fatalf("generation = %d, want %d -- the file grew, but the prefix this cache folded is gone, so the fold was discarded and a continuation keyed to it must not be accepted", after.Epoch, folded.Epoch+1)
	}
	if after.Value.sum != 4950 {
		t.Fatalf("value = %+v, want the replacement read in full (sum 4950)", after.Value)
	}
}

// TestCache_AppendDuringTheFoldRecordsAMatchingMtime pins the other half of
// what the recorded state has to describe. When an append lands between the
// stat and the fold's read, the fold consumes the larger file and the
// recorded size follows it -- but the recorded mtime is still the one the
// stat took before the write. The next lookup then sees the size it expects
// with an mtime it does not, reads that as a same-size rewrite, and discards
// a fold nothing invalidated.
func TestCache_AppendDuringTheFoldRecordsAMatchingMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	base := countingLineExtend(t, &calls)
	stamp := time.Unix(2_000_000, 0)
	appended := false
	extend := func(ctx context.Context, p string, fromOffset int64, prior intsFold) (intsFold, int64, error) {
		if !appended {
			appended = true
			appendLines(t, p, []int{3})
			if err := os.Chtimes(p, stamp, stamp); err != nil {
				return intsFold{}, 0, err
			}
		}
		return base(ctx, p, fromOffset, prior)
	}

	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if folded.Value.sum != 6 {
		t.Fatalf("first fold = %+v, want the appended line read too (1+2+3)", folded.Value)
	}

	after, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if after.Epoch != folded.Epoch {
		t.Fatalf("generation moved to %d on a file nobody touched since the fold, want it to stay at %d -- the fold read the append, so the state it published has to describe the file it read", after.Epoch, folded.Epoch)
	}
	if after.Value.sum != 6 {
		t.Fatalf("value = %+v, want 6", after.Value)
	}
}

// TestCache_AppendRacingTheStatIsNotServedFromTheCachedFold pins that a
// decision to read nothing rests on the length the probe's own handle
// reports. The stat Get takes can predate an append that lands before the
// probe opens the file; the recorded tail still matches, because an append
// leaves it where it was, so a hit decided on that stale length returns a
// fold that is missing everything the append added.
func TestCache_AppendRacingTheStatIsNotServedFromTheCachedFold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	original := make([]int, 0, 40)
	for v := 10; v < 50; v++ {
		original = append(original, v)
	}
	writeLines(t, path, original)
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if folded.Value.sum != 1180 {
		t.Fatalf("first fold summed %d, want 1180", folded.Value.sum)
	}

	c.mu.Lock()
	st := c.epochStates[path]
	c.mu.Unlock()
	stale := tornStatInfo{FileInfo: mustStat(t, path), size: st.size, mod: st.mod}

	appended := make([]int, 0, 10)
	for v := 50; v < 60; v++ {
		appended = append(appended, v)
	}
	appendLines(t, path, appended)

	// The next Get's own stat is the one that predates the append; every
	// later look at the file sees it.
	stats := 0
	c.stat = func(p string) (os.FileInfo, error) {
		stats++
		if stats == 1 {
			return stale, nil
		}
		return os.Stat(p)
	}

	after, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after an append that raced the stat: %v", err)
	}
	if after.Value.sum != 1725 {
		t.Fatalf("value = %+v, want the appended lines read too (sum 1725) -- the probe's handle reports a longer file than the stat did, so nothing here may be served from the cached fold", after.Value)
	}
	if after.Epoch != folded.Epoch {
		t.Fatalf("generation moved to %d on a pure append, want it to stay at %d", after.Epoch, folded.Epoch)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// TestCache_DeletedPathReportsOneStableGeneration pins both halves of what a
// deletion means. The deletion itself is a discarded fold, so it advances the
// generation and the absent read has to report the number it was judged by --
// reporting 0 there says "nothing has ever happened to this path", which is
// not what happened. Every later look at the same missing path is the same
// absence, though, so it must report that same number rather than advancing
// again: a generation that moves on every request refuses every continuation
// on the request that carries it.
func TestCache_DeletedPathReportsOneStableGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nums.txt")
	writeLines(t, path, []int{1, 2})
	var calls []int64
	c := New[intsFold](8)
	ctx := context.Background()
	extend := countingLineExtend(t, &calls)
	folded, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	first, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after the delete: %v", err)
	}
	if !first.Absent {
		t.Fatal("a deleted path must read as absent")
	}
	if first.Epoch <= folded.Epoch {
		t.Fatalf("generation %d after a delete, want it past %d -- the fold this cache held is gone, and the read that says so has to be judged by the generation that says it", first.Epoch, folded.Epoch)
	}

	for i := range 4 {
		again, err := c.Get(ctx, path, extend)
		if err != nil {
			t.Fatalf("Get %d after the delete: %v", i+2, err)
		}
		if !again.Absent {
			t.Fatal("a deleted path must keep reading as absent")
		}
		if again.Epoch != first.Epoch {
			t.Fatalf("generation %d on look %d at the same missing path, want it to stay at %d -- nothing happened between these reads, and a generation that moves anyway refuses every continuation on the request that carries it", again.Epoch, i+2, first.Epoch)
		}
	}

	// An empty file is the recreate that a tombstone most easily mistakes
	// for content: its length matches the tombstone's zero, and only the
	// mtime disagrees, which is exactly the shape the same-size branches
	// read as a rewrite. A tombstone describes no content, so none of those
	// comparisons apply to it and none of their consequences should follow.
	rescansBeforeEmpty := c.Stats().FullRescans
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	empty, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after the empty recreate: %v", err)
	}
	if empty.Absent {
		t.Fatal("a recreated path must not read as absent")
	}
	if empty.Epoch != first.Epoch {
		t.Fatalf("generation %d after an empty file appeared, want the absence's %d -- the deletion was the discard; a tombstone has no content to compare this against", empty.Epoch, first.Epoch)
	}
	if got := c.Stats().FullRescans - rescansBeforeEmpty; got != 0 {
		t.Fatalf("full rescans counted %d for the first fold after a tombstone, want 0 -- nothing was cached to rescan", got)
	}

	// The path coming back with content is a new fold, and it must not be
	// mistaken for the absence that preceded it.
	rescansBeforeContent := c.Stats().FullRescans
	writeLines(t, path, []int{5, 6, 7})
	recreated, err := c.Get(ctx, path, extend)
	if err != nil {
		t.Fatalf("Get after the recreate: %v", err)
	}
	if recreated.Absent {
		t.Fatal("a recreated path must not read as absent")
	}
	if recreated.Value.sum != 18 {
		t.Fatalf("value = %+v, want the recreated content (5+6+7)", recreated.Value)
	}
	if got := c.Stats().FullRescans - rescansBeforeContent; got != 1 {
		t.Fatalf("full rescans counted %d for a real append onto a cached empty fold, want 1", got)
	}
	// The deletion is the discard, and it already moved the generation; the
	// path coming back does not discard anything further. What separates
	// "absent at 1" from "present at 1" is Absent, which a continuation
	// carries and compares. What must NOT happen is a restart at 0, which
	// would read as a path nothing had ever happened to.
	if recreated.Epoch != first.Epoch {
		t.Fatalf("generation %d after the path came back, want the absence's %d -- the deletion was the discard, and a recreate must not restart the count", recreated.Epoch, first.Epoch)
	}
	if recreated.Epoch == 0 {
		t.Fatal("a path that was deleted and recreated must not report the generation of a path nothing has happened to")
	}
}
