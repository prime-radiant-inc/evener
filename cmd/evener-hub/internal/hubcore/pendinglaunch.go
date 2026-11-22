package hubcore

import (
	"context"
	"errors"
	"time"

	"primeradiant.com/evener/rendezvous"
)

// ErrResumeLocksClosed reports a launch asked for after the hub stopped
// accepting them. It is retryable in the sense that the hub is going away, not
// that another attempt can succeed here.
var ErrResumeLocksClosed = errors.New("resume locks closed")

// PendingLaunch is one resume child that has started but has not registered
// yet. It lives beside the per-session resume locks and, unlike the caller that
// started it, it outlives that caller: a browser that navigates away or a
// dropped connection leaves the healthy child restoring, and this claim both
// keeps the next caller from spawning a duplicate and bounds the child by the
// resume budget. The claim is dropped the moment the child's rendezvous entry
// publishes, or when the child is reaped on exit, deadline, or hub shutdown.
type PendingLaunch struct {
	owner     *ResumeLocks
	sessionID string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	// The fields below are guarded by owner.mu. entry and err are read only
	// after done closes, which orders them with the settle that wrote them.
	settled  bool
	detached bool
	entry    rendezvous.Entry
	err      error
}

// ClaimPendingLaunch returns the session's in-flight launch, creating one
// bounded by ttl when none exists. created reports that the caller owns the
// launch and must settle it; a false return means another launch owns the child
// and the caller may await it instead.
func (r *ResumeLocks) ClaimPendingLaunch(sessionID string, ttl time.Duration) (claim *PendingLaunch, created bool) {
	if r == nil || sessionID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingLaunches == nil {
		r.pendingLaunches = map[string]*PendingLaunch{}
	}
	if existing, ok := r.pendingLaunches[sessionID]; ok {
		return existing, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	claim = &PendingLaunch{owner: r, sessionID: sessionID, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if r.closed {
		claim.settleLocked(rendezvous.Entry{}, ErrResumeLocksClosed)
		return claim, false
	}
	r.pendingLaunches[sessionID] = claim
	return claim, true
}

// PendingLaunch reports the session's in-flight launch, if any.
func (r *ResumeLocks) PendingLaunch(sessionID string) (*PendingLaunch, bool) {
	if r == nil || sessionID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	claim, ok := r.pendingLaunches[sessionID]
	return claim, ok
}

// HasPendingLaunch reports whether a launch is in flight for sessionID.
func (r *ResumeLocks) HasPendingLaunch(sessionID string) bool {
	_, ok := r.PendingLaunch(sessionID)
	return ok
}

// Context bounds the launch: its deadline is the claim's budget, and Close
// cancels it so a supervisor reaps the child on shutdown.
func (p *PendingLaunch) Context() context.Context { return p.ctx }

// Cancel ends the launch's budget early, which makes its supervisor reap the
// child and settle the claim. Force Stop uses it for a child that no
// ActiveResume covers: one whose caller walked away mid-restore.
func (p *PendingLaunch) Cancel() { p.cancel() }

// Await waits for the launch to publish or be reaped, or for ctx to end.
func (p *PendingLaunch) Await(ctx context.Context) (rendezvous.Entry, error) {
	select {
	case <-p.done:
	case <-ctx.Done():
		return rendezvous.Entry{}, ctx.Err()
	}
	if p.err != nil {
		return rendezvous.Entry{}, p.err
	}
	return p.entry, nil
}

// HandOff transfers the live child to a detached supervisor. A launch that
// detached is settled by that supervisor, never by the caller returning.
func (p *PendingLaunch) HandOff() {
	p.owner.mu.Lock()
	p.detached = true
	p.owner.mu.Unlock()
}

// Detached reports whether a supervisor owns the child.
func (p *PendingLaunch) Detached() bool {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	return p.detached
}

// Settle publishes the launch's outcome and drops the claim. It is idempotent:
// a shutdown and the child's supervisor may both arrive here.
func (p *PendingLaunch) Settle(entry rendezvous.Entry, err error) {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if current, ok := p.owner.pendingLaunches[p.sessionID]; ok && current == p {
		delete(p.owner.pendingLaunches, p.sessionID)
	}
	p.settleLocked(entry, err)
}

// settleLocked records the outcome once and wakes everyone waiting on it.
// Callers hold owner.mu.
func (p *PendingLaunch) settleLocked(entry rendezvous.Entry, err error) {
	if p.settled {
		return
	}
	p.settled = true
	p.entry, p.err = entry, err
	p.cancel()
	close(p.done)
}

// Close refuses new launches and cancels every in-flight one, then waits for
// each to settle. A supervisor settles by reaping its child, so this is what
// keeps hub shutdown from leaking a restoring child or its goroutine.
func (r *ResumeLocks) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	claims := make([]*PendingLaunch, 0, len(r.pendingLaunches))
	for _, claim := range r.pendingLaunches {
		claims = append(claims, claim)
	}
	r.mu.Unlock()
	for _, claim := range claims {
		claim.cancel()
	}
	for _, claim := range claims {
		<-claim.done
	}
}
