// Package promoter turns a discovered fuzzing failure into a permanent,
// deduplicated, flake-guarded regression test. It is the serf-agnostic core of
// the fuzzing toolkit: it imports only the standard library, and a project plugs
// in four hooks (the Adapter) to describe its own surfaces. The load-bearing
// discipline is flake-guard-before-promote — a failure earns a regression test
// only if it reproduces deterministically K times with the same signature;
// anything else is quarantined, never emitted.
package promoter

import (
	"context"
	"encoding/json"
	"fmt"
)

// OracleTag classifies why a failure is a failure. The tag set is the portable
// oracle taxonomy; a surface picks the tags that apply to it.
type OracleTag string

const (
	// Panic is the floor oracle: the seam crashed.
	Panic OracleTag = "panic"
	// Invariant is a named semantic invariant that did not hold.
	Invariant OracleTag = "invariant"
	// ErrorShape is a validate/handler divergence (validation said clean but the
	// handler misbehaved, or vice versa).
	ErrorShape OracleTag = "error-shape"
	// Wedge is a hang/timeout: the seam never made progress.
	Wedge OracleTag = "wedge"
	// HTTP5xx is a server-side error response that should not be reachable.
	HTTP5xx OracleTag = "http-5xx"
	// PathEscape is an input that escaped its intended path/root sandbox.
	PathEscape OracleTag = "path-escape"
)

// Failure is a discovered failure, surface-agnostic. Artifact is the MINIMIZED
// reproducer (op-list + seed + inputs), encoded however the adapter chooses.
type Failure struct {
	Surface  string          // "appwire-seq", "toolargs", ...
	Oracle   OracleTag       // which oracle fired
	Stack    []string        // normalized frames (project-relative), for panic dedup
	Detail   string          // invariant name / panic message / divergence description
	Artifact json.RawMessage // the minimized reproducer, adapter-defined
}

// Signature buckets failures: same bug ⇒ same signature. Oracle plus a
// normalized discriminator Key. For Panic the adapter sets Key to the top-N
// stack frames; for semantic oracles (Invariant/ErrorShape/...) there is no
// distinguishing stack, so the adapter must fold the named invariant / Detail
// into Key — otherwise every failure of that oracle collapses into one bucket
// and distinct bugs are silently dropped as already-known.
type Signature struct {
	Oracle OracleTag
	Key    string
}

// String renders the signature as a stable bucket id.
func (s Signature) String() string {
	return string(s.Oracle) + ":" + s.Key
}

// Adapter is the project-supplied glue — the four hooks that make the promoter
// portable. Everything else (flake-guard loop, bucket store, commit) is generic.
type Adapter interface {
	// Minimize is usually a passthrough — rapid/go-fuzz already minimized. The
	// hook exists for surfaces whose runner does not shrink.
	Minimize(Failure) Failure
	// Signature computes the dedup key (lets a project normalize stacks and fold
	// in the discriminator its own way).
	Signature(Failure) Signature
	// Replay rebuilds the seam and re-runs the artifact. It returns whether the
	// failure reproduced (failed) and whether it reproduced as the SAME bug
	// (sameSignature). Both must be true on every flake-guard iteration for a
	// failure to be promoted.
	Replay(context.Context, Failure) (failed bool, sameSignature bool)
	// Emit renders and writes the regression test, returning the path written.
	Emit(Failure) (path string, err error)
}

// Quarantiner records a failure that did not survive the flake-guard. The
// artifact is logged for human inspection; it is never written as a test.
type Quarantiner interface {
	Quarantine(f Failure, survivedRuns int) error
}

// Outcome is the result of a promote attempt.
type Outcome int

const (
	// Promoted: the failure survived the flake-guard and a regression test was
	// written.
	Promoted Outcome = iota + 1
	// AlreadyKnown: a regression test for this signature already exists.
	AlreadyKnown
	// Quarantined: the failure did not reproduce deterministically K times with
	// the same signature; it was logged, not emitted.
	Quarantined
)

// String names the outcome.
func (o Outcome) String() string {
	switch o {
	case Promoted:
		return "promoted"
	case AlreadyKnown:
		return "already-known"
	case Quarantined:
		return "quarantined"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Promoter runs the failure→regression discipline against one project's Adapter.
type Promoter struct {
	adapter Adapter
	store   *BucketStore
	log     Quarantiner
	// K is the number of deterministic same-signature replays a failure must
	// pass before it is promoted (research §5.4; start at 5).
	K int
}

// New builds a Promoter. K defaults to 5 if non-positive.
func New(adapter Adapter, store *BucketStore, log Quarantiner, k int) *Promoter {
	if k <= 0 {
		k = 5
	}
	return &Promoter{adapter: adapter, store: store, log: log, K: k}
}

// Promote applies the load-bearing discipline: dedup against committed buckets,
// flake-guard with K deterministic same-signature replays, then emit. A failure
// that does not reproduce identically all K times is quarantined and never
// becomes a test — this is the rule that keeps the gate trustworthy.
func (p *Promoter) Promote(ctx context.Context, f Failure) (Outcome, error) {
	f = p.adapter.Minimize(f)
	sig := p.adapter.Signature(f)

	if p.store.Has(sig) {
		return AlreadyKnown, nil // already has a committed regression test
	}

	for i := 0; i < p.K; i++ {
		failed, same := p.adapter.Replay(ctx, f)
		if !failed || !same {
			// Non-deterministic or signature-shifting: quarantine, never emit.
			if err := p.log.Quarantine(f, i); err != nil {
				return Quarantined, err
			}
			return Quarantined, nil
		}
	}

	path, err := p.adapter.Emit(f)
	if err != nil {
		return 0, fmt.Errorf("emit regression test: %w", err)
	}
	if err := p.store.Add(sig, path); err != nil {
		return 0, fmt.Errorf("record bucket: %w", err)
	}
	return Promoted, nil
}
