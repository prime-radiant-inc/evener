// Package clock is the agent core's sole source of time. It exists so the whole
// turn / job / goal lifecycle can run on an injectable clock: production wires
// in Real() (thin wrappers over the standard library); tests inject a fake that
// advances virtual time deterministically.
//
// The interface lives in this dependency-light leaf package — not in package
// agent — so that agent/internal/agenttest can name the Timer/Ticker return
// types to implement a fake Clock without importing package agent (which would
// form a test import cycle). Everything here depends only on the standard
// library.
package clock

import "time"

// Clock abstracts every source of time the agent core reads or waits on: the
// wall clock (Now), blocking sleeps, and the timer/ticker/callback primitives
// the jobs and watchdog paths arm. A real clock delegates to the standard
// library; a fake advances virtual time on command.
type Clock interface {
	// Now reports the current time.
	Now() time.Time
	// Sleep blocks for at least d (or until the fake clock is advanced past d).
	Sleep(d time.Duration)
	// After returns a channel that receives the current time after d.
	After(d time.Duration) <-chan time.Time
	// AfterFunc schedules f to run in its own goroutine after d, returning a
	// Timer that can stop the pending call. The returned Timer's C never fires
	// (matching time.AfterFunc).
	AfterFunc(d time.Duration, f func()) Timer
	// NewTimer creates a Timer that fires once after d.
	NewTimer(d time.Duration) Timer
	// NewTicker creates a Ticker that fires repeatedly every d.
	NewTicker(d time.Duration) Ticker
}

// Timer mirrors *time.Timer behind a method-based channel accessor so a fake can
// satisfy it. Stop/Reset carry the standard-library semantics.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Ticker mirrors *time.Ticker behind a method-based channel accessor.
type Ticker interface {
	C() <-chan time.Time
	Stop()
	Reset(d time.Duration)
}

// Real returns a Clock backed by the standard library.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return &realTimer{t: time.AfterFunc(d, f)}
}
func (realClock) NewTimer(d time.Duration) Timer   { return &realTimer{t: time.NewTimer(d)} }
func (realClock) NewTicker(d time.Duration) Ticker { return &realTicker{t: time.NewTicker(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time   { return r.t.C }
func (r *realTicker) Stop()                 { r.t.Stop() }
func (r *realTicker) Reset(d time.Duration) { r.t.Reset(d) }
