# Goal engine v2 — direction note

**Status:** Direction note only — NOT a reviewed spec. Captures the four v2 directions
queued from the 2026-06-12 e2e campaign punch list before they are lost. Needs (a) the
live stop-hook incident detail filled in by Jesse, (b) brainstorm + /par before any
implementation. · **Date:** 2026-06-12 · **Branch:** `job-control-spec`

## Provenance

These four directions were harvested as "lessons from a live stop-hook incident
2026-06-12" during the job-control e2e campaign. The incident's narrative detail
(which goal, what the stop hook judged, what the agent claimed, what it cost) was not
committed to the ledger and is recoverable only from the live session on Jesse's
primary machine. **The direction one-liners below are verbatim from the campaign
handoff; the elaborations are proposed readings against the current code and specs,
to be confirmed or corrected before speccing.**

## Baseline (goal v1, shipped)

Per `docs/superpowers/specs/2026-06-06-serf-goal-design.md` (revision 4 + amendments):
the model self-declares completion via `update_goal("complete"|"blocked")` behind an
evidence-audit prompt; the sole automatic stop is the two-tier no-progress breaker
(`NoProgressLimit` 3 / `NeverProgressedLimit` 6); state persists in
`SessionMeta.Goal`; compaction re-injects the objective. Completion truth therefore
rests on the model's claim plus a prompt-level audit — nothing machine-checks the
claim against durable state.

## The four directions

1. **"Conditions as queries over durable state."** Reading: a goal can carry
   machine-evaluable conditions expressed as queries over serf's durable records
   (jobstore job records, terminal events, watch state) instead of — or gating —
   the model's self-declaration. The job-control contract's durable-reconstruction
   invariants (`docs/job-control.md` § "Durable reconstruction invariants") are the
   natural substrate: a condition that can be evaluated by replaying/querying durable
   state cannot be satisfied by narrative alone.

2. **"Quiet-goal watchdog (F4 pointed at the goal lane)."** Reading: apply the F4
   quiet-job watchdog mechanism (surface-ergonomics spec
   `2026-06-11-job-control-surface-ergonomics.md` F4: quiet window → one owner
   notification per quiet stretch, reset on activity) to active goals. A goal lane
   that has gone quiet — continuation turns not advancing, or no goal-attributable
   activity for the window — notifies the owner instead of silently stalling until
   the no-progress breaker fires. The breaker bounds runaway; the watchdog surfaces
   silence. They are complements, not substitutes.

3. **"Stop-claims verified rather than worlds re-derived."** Reading: when the model
   issues a stop-claim (`update_goal("complete")`, Stop-hook "done" judgment), v2
   verifies that specific claim — e.g. evaluates the goal's durable-state conditions
   (direction 1) — rather than re-deriving the whole world to audit the turn. Verification
   cost scales with the claim, not with session size; a false claim is rejected with
   the failing condition named, which is also better steering than a generic
   "evidence insufficient".

4. **"Delta feedback."** Reading: continuation turns feed the model what *changed*
   since the last goal evaluation (condition flips, new terminal events, new output)
   rather than re-presenting full state. Pairs with direction 1: queries over durable
   state have well-defined deltas. Lower token cost per continuation turn and less
   erosion of the objective under compaction.

## Constraints carried from v1 (do not regress)

- The no-progress breaker remains the backstop; nothing in v2 may create an unbounded
  goal (v1 amendment: "no iteration cap" — the breaker is the sole automatic stop).
- Goal state remains durable across resume (`SessionMeta.Goal` semantics).
- Capability gating: the goal surface stays gated as in v1.

## Deferred / out of scope here

No schema for condition queries, no watchdog window value, no delta encoding — those
are spec-time decisions. This note exists so the directions survive the host handoff;
it makes no implementation commitments.
