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

// AdmissionStats exposes bounded workload counts without principal identities.
type AdmissionStats struct {
	InFlight   int    `json:"inFlight"`
	Active     int    `json:"active"`
	Queued     int    `json:"queued"`
	PeakActive int    `json:"peakActive"`
	PeakQueued int    `json:"peakQueued"`
	Completed  uint64 `json:"completed"`
}

type admission struct {
	ingress  map[principalKey]int
	inFlight int
	// onChange is a synchronous observation/fault boundary installed before use.
	// It must not call back into admission or retain caller authority.
	onChange  func(AdmissionStats)
	completed uint64

	mu                    sync.Mutex
	active                int
	principals            map[principalKey]int
	queue                 []*admissionWaiter
	closed                bool
	peakActive, peakQueue int
}

func newAdmission() *admission {
	return &admission{principals: make(map[principalKey]int), ingress: make(map[principalKey]int)}
}
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
		a.observe()
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
					a.observe()
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
	a.observe()
}
func (a *admission) releaseLocked(key principalKey) {
	a.active--
	a.completed++
	a.principals[key]--
	if a.principals[key] == 0 {
		delete(a.principals, key)
	}
	a.observe()
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

func (a *admission) statsLocked() AdmissionStats {
	return AdmissionStats{InFlight: a.inFlight, Active: a.active, Queued: len(a.queue), PeakActive: a.peakActive, PeakQueued: a.peakQueue, Completed: a.completed}
}
func (a *admission) stats() AdmissionStats { a.mu.Lock(); defer a.mu.Unlock(); return a.statsLocked() }
func (a *admission) observe() {
	if a.onChange != nil {
		a.onChange(a.statsLocked())
	}
}

// reserveIngress counts body readers as well as active/queued SDK calls. A
// single authenticated principal cannot occupy every ingress slot by streaming
// incomplete bodies; the remaining global slots stay available to other keys.
func (a *admission) reserveIngress(key principalKey) (func(), error) {
	a.mu.Lock()
	if a.closed || a.ingress[key] >= 132 {
		a.mu.Unlock()
		return nil, &DomainError{Code: Busy, Retryable: true}
	}
	a.ingress[key]++
	a.inFlight++
	a.observe()
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.ingress[key]--
			a.inFlight--
			if a.ingress[key] == 0 {
				delete(a.ingress, key)
			}
			a.observe()
		})
	}, nil
}
