// Package fault injects deterministic, fuzzer-driven faults into the effect
// seams a harness already owns — an afero.Fs or an http.RoundTripper — so a
// fuzz target can drive the error-handling branches that adversarial *input*
// alone cannot reach: a disk write that fails, a mid-stream network drop, a
// permission error. Those branches (the long tail of `if err != nil { ... }`)
// are where real bugs hide and where unit tests rarely go.
//
// The design mirrors serf's Clock and afero seams: a nil *Schedule injects
// nothing, so a decorated seam is a byte-identical pass-through — zero cost when
// absent, safe to leave in a default construction path. Faults come only from a
// Schedule a test builds explicitly from fuzzer bytes, and the failure pattern
// is a deterministic function of those bytes (no RNG), so a crash always
// reproduces from its corpus entry.
//
// This package is serf-agnostic — it decorates only stdlib/third-party
// interfaces and imports no primeradiant.com/serf package, per the fuzz module's
// portability boundary.
package fault

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
)

// ErrInjected is the generic injected fault. Callers that only need "does this
// code survive an error here" can match against it; the schedule also draws from
// a small set of typed errors (below) so branches that switch on the error kind
// get exercised too.
var ErrInjected = errors.New("fault: injected failure")

// injectable is the set of representative, genuinely fire-able errors a Schedule
// draws from, so branches that type-switch on the error kind — os.IsNotExist, an
// unexpected EOF mid-decode, a deadline — are reached, not just the generic path.
var injectable = []error{
	ErrInjected,
	io.ErrUnexpectedEOF,
	os.ErrPermission,
	os.ErrNotExist,
	context.DeadlineExceeded,
}

// Schedule decides which sequential operations fail, deterministically, from a
// plan of fuzzer bytes. The zero value and nil both inject nothing. It is safe
// for concurrent use: streaming seams call it from multiple goroutines.
type Schedule struct {
	plan []byte
	n    atomic.Uint64
}

// FromBytes derives a Schedule from fuzzer bytes. An empty plan injects no
// faults, so FromBytes(nil) is equivalent to no schedule at all.
func FromBytes(plan []byte) *Schedule { return &Schedule{plan: plan} }

// Active reports whether the schedule can inject any fault. A decorator over an
// inactive schedule returns the base seam unchanged.
func (s *Schedule) Active() bool { return s != nil && len(s.plan) > 0 }

// trip advances the operation counter and returns the error this operation
// should fail with, or nil to let it proceed. Faults are sparse by construction
// (~1 in 4 ops), so the success paths still run and the fuzzer explores
// interleavings of failure and progress rather than failing everything at once.
func (s *Schedule) trip() error {
	if !s.Active() {
		return nil
	}
	i := s.n.Add(1) - 1
	b := s.plan[int(i%uint64(len(s.plan)))]
	if b%4 != 0 {
		return nil
	}
	return injectable[int(b>>2)%len(injectable)]
}

// RoundTripper wraps base so that scheduled requests return a transport error
// instead of reaching base. A real http.Client surfaces transport failures as a
// non-nil error with a nil response; this honors that contract, including
// draining and closing the request body. A nil/inactive schedule returns base.
func RoundTripper(base http.RoundTripper, s *Schedule) http.RoundTripper {
	if !s.Active() {
		return base
	}
	return &faultRT{base: base, s: s}
}

type faultRT struct {
	base http.RoundTripper
	s    *Schedule
}

func (f *faultRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := f.s.trip(); err != nil {
		// Honor the RoundTripper contract even on the fault path: a transport
		// that errors must still consume and close the request body. We return
		// the raw error, exactly as a real RoundTripper does — http.Client is
		// what wraps it in a *url.Error, so callers that go through a Client see
		// the same shape they would in production.
		if req.Body != nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
		}
		return nil, err
	}
	return f.base.RoundTrip(req)
}
