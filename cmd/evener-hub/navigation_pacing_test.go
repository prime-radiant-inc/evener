package hub

import (
	"context"
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

// The boundary branch must shorten its park to the retry deadline: with a
// 24h boundary and retryAfter far shorter, the loop's second park is the
// retry window, not the boundary.
func TestNavigationBoundaryParkShortensToRetryDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.nextBoundary = now.Add(24 * time.Hour)
	retitling := &perCaptureRetitleSource{inner: source}
	created := make(chan *fakeNavigationTimer, 16)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Source = retitling
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
	source.mu.Lock()
	source.captured = make(chan struct{}, 8)
	// Fail the first forced capture only: err set before Invalidate, cleared
	// after the first failed capture is observed.
	source.err = &staticError{}
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go service.Start(ctx)
	<-created // boundary park (24h minus elapsed)
	source.changeTitle("renamed")
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	<-source.captured // failed forced capture
	source.mu.Lock()
	source.err = nil
	source.mu.Unlock()
	// The next park after the failure must be the retry deadline (~10ms),
	// not the 24h boundary.
	select {
	case timer := <-created:
		if d := 24*time.Hour - timer.delay; d < 0 || d > time.Second {
			t.Fatalf("post-failure park = %v, want ~retryAfter (10ms), not the boundary", timer.delay)
		}
	case <-time.After(time.Second):
		t.Fatal("no park after failed forced refresh")
	}
}
