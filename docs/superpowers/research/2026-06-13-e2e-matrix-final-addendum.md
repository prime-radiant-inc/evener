# E2E matrix final addendum — head_bytes strict-zero fix caught + closed, 14/14 on the final surface

**Date:** 2026-06-13 · **HEAD:** `f563b2ca` (after the strict-zero fix) ·
**Model:** `openai/gpt-5.5` (OAuth `openai`) · **Result:** **14/14 PASS** ·
**Ledger:** `2026-06-13-e2e-matrix-final-ledger.md` (this directory).

This is the live matrix on the **final job-control surface** — the
`max_wait_ms` unification + `head_bytes` on `job_read_output` + the strict-zero
fix below. It is the gate for recursion T9–18 (plan decision #1; spec §9).

## The regression this matrix caught (and closed)

The first attempt at this matrix aborted on card 1. Root cause was a real
product bug that unit-level testing had missed: **`head_bytes`/`tail_bytes` did
not follow the strict-zero rule** the rest of the surface uses (`max_wait_ms`:
`0` = unset). `gpt-5.5` fills *every* schema property with `0`, so it sent
`head_bytes:0, tail_bytes:0` on every `job_read_output` call. The
mutual-exclusion check (`hasHead && hasTail`, presence-based) and
`boundedJobBytesArg` (`value <= 0 → error`) both treated an explicit `0` as
"set" → `invalid_request` on every call → an infinite retry loop, card unusable.

**Fix (`f563b2ca`):** `strictZeroJobBytesArg` maps `0`/absent → unset,
positive → capped at `maxJobOutputBytes`, negative → `invalid_request`
(mirrors `max_wait_ms`). Mutual exclusion now fires only when both are
positive. Dead `boundedJobBytesArg` deleted; its cap test repointed.

## Verification (against primary artifacts, not the runner's word)

- Card 1's transcript shows `gpt-5.5` sent `head_bytes:0` (×2) and
  `tail_bytes:0` (×1) — the exact shapes that looped before — plus genuine
  `head_bytes:100` / `tail_bytes:65536` reads, with **zero**
  `head_bytes and tail_bytes are mutually exclusive` errors in the session.
  The fix holds at the real model layer.
- Hub log: **zero** panics, data races, or `invalid_request` spam across the
  whole run.
- Card 6 arm (b) is INCONCLUSIVE-by-design (the call-time args-schema gate
  rejected `count:"banana"`, child retried valid) — the documented
  call-time-gate variant, not a product defect (same disposition as the
  2026-06-12 run).

## Runner-procedure lesson (folded into the runbook for next time)

The aborted first attempt ground ~40 minutes on card 1's loop and batched its
ledger to the end (so progress was invisible). This run added two runner-prompt
disciplines that made it reliable:

1. **Ledger-per-card** — write each verdict immediately; never batch. A crash
   then shows exactly how far the run got.
2. **Circuit-breaker** — if a card's session emits the same tool error 5+ times
   in a row, record FAIL-with-evidence and move on; never grind a loop. A
   runaway loop is itself a reportable product finding, captured not ground.

Headline takeaway: the live matrix is load-bearing precisely *because* real
providers exercise argument shapes (every-property-filled-with-`0`) that
unit tests with hand-authored args do not.
