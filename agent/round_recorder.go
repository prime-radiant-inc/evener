package agent

import (
	"slices"
	"time"

	"primeradiant.com/evener/llm"
)

// attemptRecord is one stream attempt's outcome inside a retry group: how it
// failed, how long it ran, and how much of the answer it managed to deliver.
type attemptRecord struct {
	Phase         llm.AttemptPhase
	Err           error
	Duration      time.Duration
	ContentWindow time.Duration
	SalvagedBytes int
}

// groupRecord is one retry group — a single callModel invocation against one
// model — with every attempt it burned and the best partial any of them left
// behind. Best is the largest salvaged-byte snapshot, not the latest: a retry
// that trickled must never replace the attempt that streamed most of the answer.
type groupRecord struct {
	Model, Provider string
	Attempts        []attemptRecord
	BestPartial     *llm.Response
	BestBytes       int
	// Elapsed is the group's own wall time on the session clock, from its
	// first attempt to its return: the unit a quota wait's elapsed sample
	// reports, so a fallback that hits quota after the primary failed is not
	// billed the primary's time.
	Elapsed time.Duration
}

// observe folds one attempt into the group. Called per attempt from inside
// callModel's retry closure, so the snapshot is taken before RetryStream's
// OnReset discards the partial for the next try.
func (g *groupRecord) observe(rec attemptRecord, partial *llm.Response) {
	g.Attempts = append(g.Attempts, rec)
	// Salvage is what a FAILED attempt left behind; a successful attempt's
	// response is the round's answer, not salvage. Any nonzero salvage counts —
	// there is no byte floor, and a reasoning-only partial (zero salvaged
	// bytes) is never salvage.
	if rec.Err == nil || partial == nil || rec.SalvagedBytes <= 0 {
		return
	}
	if rec.SalvagedBytes > g.BestBytes {
		g.BestBytes = rec.SalvagedBytes
		g.BestPartial = partial
	}
}

// hasConsumePhaseFailure reports whether any attempt in the group failed after
// the stream opened — a mid-stream death or an accept-then-silence stall. The
// zero-content-fast shapes (open-phase rejections, in-band fast rejects) are
// request rejections, not a broken stream.
func (g *groupRecord) hasConsumePhaseFailure() bool {
	for _, a := range g.Attempts {
		if a.Err != nil && (a.Phase == llm.PhaseConsume || a.Phase == llm.PhaseSilentStall) {
			return true
		}
	}
	return false
}

// roundRecorder aggregates every retry group one round ran — the primary, the
// continuation-recovery retry, and each model fallback — so the settlement path
// can see what the model actually produced before the round failed.
type roundRecorder struct{ Groups []groupRecord }

// BestSalvage returns the largest partial the round produced and the group that
// produced it, or (nil, nil) when nothing was salvaged. Selection spans ALL
// groups: a fallback group that failed with a trickle must not shadow the
// primary group's far larger partial.
//
// The returned group is an INTERIOR POINTER into Groups, so it stays valid only
// until the next append: appending a fallback group can relocate the backing
// array and leave the pointer addressing a stale copy. Read what you need off
// it (model, provider, partial) before the round appends another group; never
// stash it across a callModel invocation.
//
// The constraint binds inside callModelWithFallback's chain walk, the only
// place Groups is appended: its continuation-recovery and fallback-loop
// group-transition resets both call this immediately before appending a group,
// and both keep the result if-scoped and merely nil-test it. A mid-walk caller
// that instead held the pointer across its append is the hazard this documents.
func (r *roundRecorder) BestSalvage() (partial *llm.Response, from *groupRecord) {
	if r == nil {
		return nil, nil
	}
	var best *groupRecord
	for i := range r.Groups {
		g := &r.Groups[i]
		if g.BestPartial == nil {
			continue
		}
		if best == nil || g.BestBytes > best.BestBytes {
			best = g
		}
	}
	if best == nil {
		return nil, nil
	}
	return best.BestPartial, best
}

// SteeringGroup returns the group the round's failure steering should describe:
// the salvage-producing group, else the last group that failed in the consume
// phase, else nil. Never "whichever group failed last" — a chain walk ending on
// an open-phase fallback rejection must still describe the group whose stream
// actually broke.
//
// Like BestSalvage, the result is an interior pointer into Groups and is only
// valid until the next append; see BestSalvage for the constraint.
func (r *roundRecorder) SteeringGroup() *groupRecord {
	if r == nil {
		return nil
	}
	if _, from := r.BestSalvage(); from != nil {
		return from
	}
	for i := range slices.Backward(r.Groups) {
		if r.Groups[i].hasConsumePhaseFailure() {
			return &r.Groups[i]
		}
	}
	return nil
}

// HasConsumePhaseFailure reports whether any group in the round failed after
// its stream opened.
func (r *roundRecorder) HasConsumePhaseFailure() bool {
	if r == nil {
		return false
	}
	for i := range r.Groups {
		if r.Groups[i].hasConsumePhaseFailure() {
			return true
		}
	}
	return false
}

// beginRoundRecorder installs a fresh recorder for the round about to run. Per
// round, not per turn: a round that fails before its own model call must never
// settle on the previous round's salvage.
func (s *Session) beginRoundRecorder() {
	rec := &roundRecorder{}
	s.mu.Lock()
	s.currentRoundRecorder = rec
	s.mu.Unlock()
}

// roundSalvageRecorder returns the round's recorder — what the model call
// records into and what settlement reads. Never nil: the round loop installs
// one per round, and a caller outside the loop (a direct model call) gets an
// empty one installed on first use, which reads as "nothing salvaged".
func (s *Session) roundSalvageRecorder() *roundRecorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentRoundRecorder == nil {
		s.currentRoundRecorder = &roundRecorder{}
	}
	return s.currentRoundRecorder
}

// quotaGroupSample is the model and elapsed time of one retry group — the
// pair a quota wait reports. Both come from the same group, so a fallback
// that meets the limit is neither billed the primary's runtime nor reported
// under the primary's name.
type quotaGroupSample struct {
	Model   string
	Elapsed time.Duration
}

// quotaGroupFacts is the sample of the last retry group the round ran — the
// group whose error the round holds. Each callModel invocation stamps its
// own group's model and Elapsed, and the round's error always comes from the
// most recently appended group, so the last entry is the quota error's group
// even when a fallback produced it. Empty when the round ran no group (the
// error came from request preparation or admission).
func quotaGroupFacts(recorder *roundRecorder) quotaGroupSample {
	if recorder == nil || len(recorder.Groups) == 0 {
		return quotaGroupSample{}
	}
	g := recorder.Groups[len(recorder.Groups)-1]
	return quotaGroupSample{Model: g.Model, Elapsed: g.Elapsed}
}
