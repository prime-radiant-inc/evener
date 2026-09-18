package interactiveartifacts

import (
	"context"
	"sync"
)

type principalKey struct{ realm, principal string }
type admissionWaiter struct {
	key      principalKey
	ready    chan struct{}
	admitted bool
}
type admission struct {
	mu                    sync.Mutex
	active                int
	principals            map[principalKey]int
	queue                 []*admissionWaiter
	closed                bool
	peakActive, peakQueue int
}

func newAdmission() *admission { return &admission{principals: make(map[principalKey]int)} }
func (a *admission) acquire(ctx context.Context, key principalKey) (func(), error) {
	a.mu.Lock()
	if a.closed || ctx.Err() != nil {
		a.mu.Unlock()
		return nil, &DomainError{Code: Busy, Retryable: true}
	}
	w := &admissionWaiter{key: key, ready: make(chan struct{})}
	if a.active < 32 && a.principals[key] < 4 {
		a.admit(w)
	} else {
		if len(a.queue) == 128 {
			a.mu.Unlock()
			return nil, &DomainError{Code: Busy, Retryable: true}
		}
		a.queue = append(a.queue, w)
		a.peakQueue = max(a.peakQueue, len(a.queue))
	}
	a.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	a.mu.Lock()
	if ctx.Err() != nil || a.closed {
		if w.admitted {
			a.releaseLocked(key)
		} else {
			for i, candidate := range a.queue {
				if candidate == w {
					a.queue = append(a.queue[:i], a.queue[i+1:]...)
					break
				}
			}
		}
		a.mu.Unlock()
		return nil, &DomainError{Code: Busy, Retryable: true}
	}
	a.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { a.mu.Lock(); defer a.mu.Unlock(); a.releaseLocked(key) }) }, nil
}
func (a *admission) admit(w *admissionWaiter) {
	a.active++
	a.principals[w.key]++
	a.peakActive = max(a.peakActive, a.active)
	w.admitted = true
	close(w.ready)
}
func (a *admission) releaseLocked(key principalKey) {
	a.active--
	a.principals[key]--
	if a.principals[key] == 0 {
		delete(a.principals, key)
	}
	if a.closed {
		return
	}
	// FIFO among eligible principals; skip a principal at its own limit so it
	// cannot hold unrelated clients behind its queue.
	for i := 0; i < len(a.queue) && a.active < 32; {
		w := a.queue[i]
		if a.principals[w.key] >= 4 {
			i++
			continue
		}
		a.queue = append(a.queue[:i], a.queue[i+1:]...)
		a.admit(w)
	}
}
func (a *admission) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	for _, w := range a.queue {
		close(w.ready)
	}
	a.queue = nil
}
