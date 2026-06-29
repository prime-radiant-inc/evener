package agenttest

import (
	"sync"
	"time"

	"primeradiant.com/serf/agent/internal/clock"
)

// FakeClock is a deterministically-advanceable clock.Clock for tests. Virtual
// time only moves when the test calls Advance; sleeps, timers, tickers, and
// AfterFunc callbacks all wait on virtual time. BlockUntil is the quiescence
// handshake: it blocks the test goroutine until n goroutines are parked on the
// clock, so the test can wait for a job's watchdog/finalize goroutine to arm its
// timer before advancing past the deadline — deterministically, with no
// wall-time and no race. This is the standard clockwork-style pattern, rebuilt
// minimally here so the agent module gains no new dependency.
type FakeClock struct {
	mu       sync.Mutex
	now      time.Time
	waiters  []*fakeWaiter
	blockers []*blocker
}

// fakeWaiter is one pending wait on virtual time: a timer/sleep/After delivers
// the fire time on ch; an AfterFunc runs fn; a ticker (period > 0) reschedules
// itself instead of being removed when it fires.
type fakeWaiter struct {
	until  time.Time
	ch     chan time.Time
	fn     func()
	period time.Duration
}

type blocker struct {
	count int
	ch    chan struct{}
}

// NewFakeClock returns a FakeClock started at a fixed, deterministic instant.
func NewFakeClock() *FakeClock {
	return NewFakeClockAt(time.Unix(1000, 0).UTC())
}

// NewFakeClockAt returns a FakeClock started at the given instant.
func NewFakeClockAt(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

var _ clock.Clock = (*FakeClock)(nil)

// Now reports the current virtual time.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Sleep blocks until virtual time advances by at least d.
func (f *FakeClock) Sleep(d time.Duration) {
	<-f.After(d)
}

// After returns a channel that receives the virtual time once it advances by d.
func (f *FakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.addWaiterLocked(&fakeWaiter{until: f.now.Add(d), ch: ch})
	return ch
}

// AfterFunc schedules fn to run (in its own goroutine, matching time.AfterFunc)
// once virtual time advances by d.
func (f *FakeClock) AfterFunc(d time.Duration, fn func()) clock.Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWaiter{until: f.now.Add(d), fn: fn}
	if d <= 0 {
		go fn()
		return &fakeTimer{f: f, w: w}
	}
	f.addWaiterLocked(w)
	return &fakeTimer{f: f, w: w}
}

// NewTimer creates a clock.Timer that fires once after d of virtual time.
func (f *FakeClock) NewTimer(d time.Duration) clock.Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	w := &fakeWaiter{until: f.now.Add(d), ch: ch}
	if d <= 0 {
		ch <- f.now
	} else {
		f.addWaiterLocked(w)
	}
	return &fakeTimer{f: f, w: w, ch: ch}
}

// NewTicker creates a clock.Ticker that fires every d of virtual time.
func (f *FakeClock) NewTicker(d time.Duration) clock.Ticker {
	if d <= 0 {
		panic("agenttest: non-positive interval for NewTicker")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	w := &fakeWaiter{until: f.now.Add(d), ch: ch, period: d}
	f.addWaiterLocked(w)
	return &fakeTicker{f: f, w: w, ch: ch}
}

// Advance moves virtual time forward by d, firing every waiter whose deadline
// falls in (now, now+d] in deadline order (ties broken by insertion order).
// Tickers reschedule and may fire multiple times across a single Advance.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	end := f.now.Add(d)
	for {
		idx := -1
		var earliest time.Time
		for i, w := range f.waiters {
			if w.until.After(end) {
				continue
			}
			if idx == -1 || w.until.Before(earliest) {
				idx = i
				earliest = w.until
			}
		}
		if idx == -1 {
			break
		}
		w := f.waiters[idx]
		f.now = w.until
		f.fireLocked(w)
		if w.period > 0 {
			w.until = w.until.Add(w.period)
		} else {
			f.waiters = append(f.waiters[:idx], f.waiters[idx+1:]...)
		}
	}
	f.now = end
}

// BlockUntil blocks until at least n goroutines are parked on the clock (waiting
// on a sleep, timer, ticker, or AfterFunc). It is the handshake a test uses to
// wait for background goroutines to arm their timers before advancing.
func (f *FakeClock) BlockUntil(n int) {
	f.mu.Lock()
	if len(f.waiters) >= n {
		f.mu.Unlock()
		return
	}
	b := &blocker{count: n, ch: make(chan struct{})}
	f.blockers = append(f.blockers, b)
	f.mu.Unlock()
	<-b.ch
}

// BlockedCount reports how many waiters are currently parked on the clock.
func (f *FakeClock) BlockedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

func (f *FakeClock) addWaiterLocked(w *fakeWaiter) {
	f.waiters = append(f.waiters, w)
	f.notifyBlockersLocked()
}

func (f *FakeClock) fireLocked(w *fakeWaiter) {
	if w.fn != nil {
		go w.fn()
		return
	}
	select {
	case w.ch <- f.now:
	default:
	}
}

func (f *FakeClock) notifyBlockersLocked() {
	kept := f.blockers[:0]
	for _, b := range f.blockers {
		if len(f.waiters) >= b.count {
			close(b.ch)
		} else {
			kept = append(kept, b)
		}
	}
	f.blockers = kept
}

// removeWaiter drops w from the waiter set, reporting whether it was pending.
func (f *FakeClock) removeWaiter(w *fakeWaiter) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, cur := range f.waiters {
		if cur == w {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// resetWaiter reschedules w to fire after d, re-arming it if it had fired,
// reporting whether it was still pending (matching time.Timer.Reset).
func (f *FakeClock) resetWaiter(w *fakeWaiter, d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	wasPending := false
	for _, cur := range f.waiters {
		if cur == w {
			wasPending = true
			break
		}
	}
	w.until = f.now.Add(d)
	if !wasPending {
		f.addWaiterLocked(w)
	}
	return wasPending
}

type fakeTimer struct {
	f  *FakeClock
	w  *fakeWaiter
	ch chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time        { return t.ch }
func (t *fakeTimer) Stop() bool                 { return t.f.removeWaiter(t.w) }
func (t *fakeTimer) Reset(d time.Duration) bool { return t.f.resetWaiter(t.w, d) }

type fakeTicker struct {
	f  *FakeClock
	w  *fakeWaiter
	ch chan time.Time
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               { t.f.removeWaiter(t.w) }
func (t *fakeTicker) Reset(d time.Duration) {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.w.period = d
	t.w.until = t.f.now.Add(d)
	found := false
	for _, cur := range t.f.waiters {
		if cur == t.w {
			found = true
			break
		}
	}
	if !found {
		t.f.addWaiterLocked(t.w)
	}
}
