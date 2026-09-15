package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression: persistent capture failure + one armed invalidation must be
// PACED (bounded attempts) and must not grow the pending hint.
func TestNavigationStartPacesPersistentForcedRefreshFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.nextBoundary = now.Add(24 * time.Hour)
	retitling := &perCaptureRetitleSource{inner: source}
	var attempts atomic.Int64
	wrapped := &countingSource{inner: retitling, captures: &attempts}
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Source = wrapped
		cfg.RetryAfter = 50 * time.Millisecond
		start := time.Now()
		base := time.Unix(1_700_000_000, 0).UTC()
		cfg.Now = func() time.Time { return base.Add(time.Since(start)) }
		cfg.NewTimer = func(delay time.Duration) navigationTimer {
			timer := &fakeNavigationTimer{delay: delay, ch: make(chan time.Time, 1)}
			go func() {
				time.Sleep(delay)
				select {
				case timer.ch <- time.Now():
				default:
				}
			}()
			return timer
		}
	})
	source.mu.Lock()
	source.captured = make(chan struct{}, 256)
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go service.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	source.mu.Lock()
	source.err = errBoom
	source.mu.Unlock()
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	// Let it fail persistently for 500ms.
	time.Sleep(500 * time.Millisecond)
	service.mu.Lock()
	hintLen := len(service.pendingHint.Projects)
	service.mu.Unlock()
	n := attempts.Load()
	cancel()
	if n > 40 {
		t.Fatalf("capture attempts = %d in 500ms with retryAfter=50ms — unpaced spin", n)
	}
	if hintLen != 1 {
		t.Fatalf("pending hint Projects length = %d, want 1 (no doubling)", hintLen)
	}
}

type countingSource struct {
	inner    navigationSource
	captures *atomic.Int64
}

func (c *countingSource) Revision() navigationSourceRevision { return c.inner.Revision() }
func (c *countingSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	c.captures.Add(1)
	return c.inner.Capture(ctx, generation, now)
}

var errBoom = &staticError{}

type staticError struct{}

func (*staticError) Error() string { return "persistent failure" }

// waitCapture consumes one capture signal, failing the test if none arrives.
func waitCapture(t *testing.T, captured <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for capture: %s", label)
	}
}

// waitTimer consumes one park timer, failing the test if none arrives.
func waitTimer(t *testing.T, created <-chan *fakeNavigationTimer, label string) *fakeNavigationTimer {
	t.Helper()
	select {
	case timer := <-created:
		return timer
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for park: %s", label)
		return nil
	}
}

// The boundary branch must shorten its park to the retry deadline: with a 24h
// boundary and retryAfter far shorter, a park that has a retry pending is the
// retry window, not the boundary.
//
// The failure is scripted by capture index - the second capture (the forced
// refresh for the invalidation) fails while the first (initial build) and third
// (the loop's own follow-up rebuild) succeed - so the sequence the loop walks
// is fixed. The earlier shape failed the first forced capture by clearing a
// shared err field after reading one buffered capture signal, which raced the
// forced refresh: when the refresh ran before the clear the loop correctly
// parked on the retry deadline and the test's boundary expectation failed, and
// when it ran after, the refresh succeeded and the test never exercised the
// shortening at all.
func TestNavigationBoundaryParkShortensToRetryDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.nextBoundary = now.Add(24 * time.Hour)
	source.captured = make(chan struct{}, 16)
	scripted := &scriptedFailureSource{inner: source, fail: func(call int64) bool { return call == 2 }}
	created := make(chan *fakeNavigationTimer, 16)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Source = scripted
		cfg.RetryAfter = 10 * time.Millisecond
		start := time.Now()
		base := time.Unix(1_700_000_000, 0).UTC()
		cfg.Now = func() time.Time { return base.Add(time.Since(start)) }
		cfg.NewTimer = func(delay time.Duration) navigationTimer {
			timer := &fakeNavigationTimer{delay: delay, ch: make(chan time.Time, 1)}
			created <- timer
			go func() {
				time.Sleep(delay)
				select {
				case timer.ch <- time.Now():
				default:
				}
			}()
			return timer
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go service.Start(ctx)

	// The initial build succeeds and the loop parks on the full 24h boundary.
	waitCapture(t, source.captured, "initial build")
	boundaryPark := waitTimer(t, created, "boundary park")
	if d := boundaryPark.delay; d < 24*time.Hour-time.Second {
		t.Fatalf("initial park = %v, want the 24h boundary", d)
	}

	// The invalidation's forced refresh fails, arming the retry deadline; the
	// loop's follow-up rebuild then succeeds and parks again. That park must be
	// the retry window - far below the boundary - because the retry is pending.
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	waitCapture(t, source.captured, "failed forced refresh")
	waitCapture(t, source.captured, "follow-up rebuild")
	retryPark := waitTimer(t, created, "retry-deadline park")
	if retryPark.delay > time.Second {
		t.Fatalf("post-failure park = %v, want the retry window (~retryAfter 10ms), not the 24h boundary", retryPark.delay)
	}
}

// scriptedFailureSource fails exactly the capture calls its script names,
// making multi-step retry interleavings deterministic without racing on a
// shared err field.
type scriptedFailureSource struct {
	inner navigationSource
	calls atomic.Int64
	fail  func(call int64) bool
}

func (s *scriptedFailureSource) Revision() navigationSourceRevision { return s.inner.Revision() }

func (s *scriptedFailureSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	call := s.calls.Add(1)
	snapshot, err := s.inner.Capture(ctx, generation, now)
	if err == nil && s.fail(call) {
		return navigationSourceSnapshot{}, errBoom
	}
	return snapshot, err
}

// A retry-deadline wake is NOT a time-boundary crossing. When the boundary
// park ends early because a failed forced refresh's retry came due, the loop
// must retry the pending invalidation as-is — not stamp a spurious
// navigationChangeHint{Time: true} (and bump the pending epoch) as if the
// snapshot's time boundary had elapsed.
func TestNavigationRetryDeadlineWakeDoesNotStampTimeHint(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(base)
	source.nextBoundary = base.Add(24 * time.Hour)
	source.captured = make(chan struct{}, 16)
	// Capture 1 (initial build) succeeds; capture 2 (the forced refresh for
	// the invalidation) fails and arms the retry deadline; capture 3 (the
	// scheduler's own rebuild) succeeds so the loop reaches the boundary
	// park; every later capture (the paced retries) fails.
	scripted := &scriptedFailureSource{inner: source, fail: func(call int64) bool {
		return call == 2 || call >= 4
	}}
	created := make(chan *fakeNavigationTimer, 16)
	var clockMu sync.Mutex
	now := base
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Source = scripted
		cfg.RetryAfter = time.Minute
		cfg.Now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}
		cfg.NewTimer = func(delay time.Duration) navigationTimer {
			timer := &fakeNavigationTimer{delay: delay, ch: make(chan time.Time, 1)}
			created <- timer
			return timer
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go service.Start(ctx)
	waitCapture(t, source.captured, "initial build")
	waitTimer(t, created, "boundary park")
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	waitCapture(t, source.captured, "failed forced refresh")
	waitCapture(t, source.captured, "scheduler rebuild")
	retryPark := waitTimer(t, created, "retry-deadline park")
	if retryPark.delay != time.Minute {
		t.Fatalf("retry park delay = %v, want retryAfter (1m)", retryPark.delay)
	}
	// The retry deadline comes due — a minute passed, nowhere near the 24h
	// snapshot boundary — and the park's timer fires.
	clockMu.Lock()
	now = base.Add(time.Minute + time.Second)
	clockMu.Unlock()
	retryPark.ch <- now
	waitCapture(t, source.captured, "paced retry")
	service.mu.Lock()
	hint := service.pendingHint
	service.mu.Unlock()
	if hint.Time {
		t.Fatal("retry-deadline wake stamped navigationChangeHint{Time: true}: a failed retry is not a time-boundary crossing")
	}
	if len(hint.Projects) != 1 || hint.Projects[0] != "p1" {
		t.Fatalf("pending hint Projects = %v, want the armed invalidation preserved as [p1]", hint.Projects)
	}
}
