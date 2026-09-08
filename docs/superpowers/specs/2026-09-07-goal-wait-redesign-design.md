# `/goal` waiting-vs-stuck redesign — design

**Status:** Proposed — post adversarial review (Reviewer A: 7 Important + 4 Minor; Reviewer B: 9 Important + 2 Minor; all adjudicated legitimate, no disqualifications). **Date:** 2026-09-07 · **Branch:** `goal-wait-redesign` (base `33049e4ac`) · **Provenance:** 9-strand state-of-art research (harnesses + durable execution + theory + academic), Q&A decisions (all wait sources; full semantic progress; graduated + resumable; clean-slate), Approach-1 approved with sections §§1–7 presented in chat.

> This spec supersedes the Approach-1 chat sketch where they conflict. Every adversarial finding below is closed in-spec; the disposition table in the appendix maps each finding to its fix section.

## Summary

Replace the mutation-count breaker (`NoProgressLimit=3` / `NeverProgressedLimit=6`, the sole automatic stop) with three separated mechanisms: (1) a uniform **wait registry** with a first-class persisted `waiting` state (parked goals burn zero turns and accrue zero stall signal); (2) a **progress ledger** whose stall signature is repetition-of-`(action, observation)` with no state-digest delta, keeping a total non-advancement backstop and the shipped two-tier strictness; (3) orthogonal **spend budgets** (`maxContinuations`, wall-clock `deadline`, total parked-time cap) with explicit defaults. The breaker graduates nudge → park → block with distinct verdicts, quiet goals notify per-stretch, stop-claims verify only when the goal carries conditions, and blocked goals resume via `/goal resume`. All waits are leases with required deadlines, registration validation, atomic fire-consume, and honest loss notices.

## Goals

1. A goal legitimately waiting on any source (timer, job, delegate, approval, file/HTTP/external condition, child session) parks with zero continuation turns and zero stall accrual, and wakes exactly once on fire or expiry.
2. A genuinely stalled goal (repetition without state movement, or total non-advancement past the backstop) is nudged, then parked, then blocked — with the evidence named — without false-blocking read-heavy investigation openings.
3. No goal is unbounded: every park, wake, re-park, and resume path accrues against a budget or a bound.
4. No silent strands: every lost wait, restart, overflow, or budget clear produces an honest, exactly-once notice.
5. Clean-slate store/gate with a truthful one-way v1 migration (terminals stay dropped; seeding preserves remaining-till-block).

## Non-goals

- LLM-critic / PRM stall judging (deferred behind a cost gate; the ledger is syntactic + digest-based only).
- MCP server-push wakes (2026-07-28 transports have no server-initiated requests; external push arrives via held-open stream or the cheap-poller fallback, never as a spec dependency).
- Event-sourced replay runtime (Temporal-shaped full port deferred; leases + `sclock` are the durability story).
- Token-budget accounting beyond turn/deadline/parked-time caps.

## Design

### 1. Store, states, transitions, verdict table

`Goal{objective, status, waits[], ledger, budgets, stop}` in `agent/internal/goal` (replaces `goal.go`).

- Statuses: `active | waiting | blocked | complete`. Invariant: `status==waiting` iff `len(waits)>0` **at rest**. Transients during the atomic fire step (§3) may briefly hold a consumed (fired-epoch-marked) wait while driving the wake turn; the invariant is re-established before the gate returns.
- Only `active` folds ledger outcomes and drives continuations. `waiting` skips every fold and arms nothing.
- `stop{reason, reported}`: `reported` once-gate stays (`TakeTerminalReport` semantics). Verdicts are distinct — never collapsed:
  - `"no progress"` — stall detector (repetition K or total non-advancement backstop).
  - `"budget exhausted"` — `maxContinuations` consumed (wake/evaluation turns count, §5).
  - `"deadline exceeded"` — wall-clock `deadline` passed (runs across waiting, §5).
  - `"waiting lost: <cause>"` — terminal only when a wait is lost *and* the goal cannot continue meaningfully (restart/overflow/budget-clear otherwise produce a non-terminal honest notice + re-drive, §7).
- Retarget (`/goal <new>`) clears waits, cancels their timers, resets ledger, keeps budgets. `/goal clear` clears all.
- Pure decision (restated per review M1): `decideGoalStep(snapshot, turnOutcome, predicateTruth[]) → drive | park | nudge | block`. `predicateTruth[]` is the evaluated truth of each live lease, computed outside the pure function at an explicit evaluation seam (§3). The pure table pins everything except live I/O.
- Verdict truth table (ties broken top-to-bottom):
  1. `budgets` exceeded (`maxContinuations` consumed or `deadline` passed) → `block` with the matching verdict.
  2. Any live (unfired, unexpired) wait → `park`.
  3. Freshly fired/expired wait needing its wake turn and no budget exceeded → `drive` (the atomic consume-activate-drive step, §3).
  4. Stall-K reached (two-tier, §4) → `nudge` on first reaching (stage 1), `park` with bounded auto-wait when the stall looks like waiting (stage 2, bounded by §5 re-park count), else `block` with `"no progress"`.
  5. Total non-advancement backstop reached (§4) → same graduation as (4).
  6. Else → `drive`.

### 2. Wait registry and leases

One registry in the store. Kinds: `until_time | until_job | until_delegate | until_approval | until_event(file_modified | http_match | external_label) | until_child`.

- Lease fields (persisted in full, §6): `{wait_id, kind, predicate{target ids, baseline sha/mtime, URL + matcher, approval content key, child session id}, deadline, registered_at (sclock), idempotency_key, fired_epoch (0 = live)}`.
- Defaults table: wait `timeout` required, default 10m, cap 24h; max 8 live waits per goal (shares the session's coalesced goal timer, §7 — never 8 independent timers); total parked time per goal capped (default 24h, configurable); consecutive auto-re-park bound (default 3, then graduate to block); park/wake cooldown after attach-scan-true (default 30s) to break tight park/wake cycles.
- Registration: (a) model-declared via `goal_wait(kind, predicate, timeout)`; (b) harness-auto at delegate spawn, supervised-job start, `ask_user` ask time, child-session record creation. Both paths run identical validation.
- Registration validation (fail-closed reject, mirroring `job_watch.go:278-300,529-548`): target must exist and be owned by the session tree (job id live, delegate id owned and in a wake-capable phase, file path inside the session sandbox and stat-able, URL well-formed with explicit timeout, approval content key matching a live ask, child id a known descendant). Hallucinated targets (`job_999`, unowned delegate, nonexistent file, non-pending ask, malformed URL) are rejected with the reason named — never parked.
- Predicates are queries over durable or re-derivable state only. `until_approval` keys on the stable approval content key `(header, question)` (the `askQuestion` struct has no id; `askPending` is not in `SessionMeta` and is rebuilt via `deriveRestoredAskPending`), with defined multi-ask disambiguation (all matching asks resolve = fire; record which fired). File/HTTP predicates store baselines and matcher in the lease; evaluation is frequency/timeout/sandbox-scoped (single stat or bounded fetch per evaluation tick, never a model turn per probe).
- Fire sources: attach-scan (predicate already true at registration/restore/tick), notification (job/delegate/ask/child event), timer tick (deadline or `until_time`). Exactly one fire site consumes: `claimWaitFireLocked` (the `noteConditionFireLocked` analogue, `job_watch.go:1727-1750`) marks `fired_epoch` under `goalUpdateMu` + registry lock before any kick; `idempotency_key` dedupes registration, `fired_epoch` dedupes firing. Crash between consume and kick re-drives on the next turn tail (the consume record survives; the gate sees a consumed-but-undelivered wake and drives it once).
- Wake = exactly one resume turn carrying `(wait_id, trigger excerpt, fired_at)`; the turn's first duty is a re-validation read (event is a hint, state is re-read — the inotify rule; reuse `job_watch` attach-scan/terminal-catch-up semantics). Expiry re-drives exactly one evaluation turn and never auto-blocks.
- Lost waits (restart without predicate substrate, queue overflow, budget clear, timer exhaustion) convert to an honest notice (`"waiting lost: <cause>; re-arm or proceed without the wait"`) delivered at the next turn tail, plus re-drive — never a silent strand, never a terminal lie.

### 3. Gate and lock order

Replaces `armGoalContinuation` / `settleGoalOnIdle` hold logic. Two parts: pure `decideGoalStep` (§1) + mutator that commits under `goalUpdateMu`.

- Predicate evaluation (delegate-controller / job-manager / file-stat / HTTP-match reads) is computed **before** taking `goalUpdateMu` or `s.mu` (the `hasWakePendingDependents` discipline, `session_goal.go:205-210,234-249`). Time comes from `s.sclock()` only.
- Atomic fire step under `goalUpdateMu` + registry lock: `claim fired_epoch → set status active (transient invariant suspension) → decide drive → release → kick outside the lock` (v1 §7 SetGoal/settle mutual exclusion preserved: kicks happen outside the lock; concurrent `goal_cancel_wait`/retarget/clear against a firing wait either wins before claim (no wake) or loses after claim (wake carries a `superseded:true` mark and drives a single no-op evaluation, never a duplicate work turn)).
- Settle re-checks leases live (the stale-hold philosophy, `session_goal.go:372-379`): a stale park whose predicate is already true or whose dependents drained kicks exactly once via the same claim path.
- Full lock order (all paths — register, cancel, fire, expire, settle, timer callback): `registry → goalUpdateMu → s.mu → delegate-controller / job-manager → store.mu`. Timer callbacks claim-then-wake across the same order; timers are disarmed on clear/retarget/cancel (no post-clear stale fire — the `goalDependentsHeld`-voiding analogue).
- `decideGoalStep` inputs make the §7 pure tables writable: snapshot (status, waits with truth array, ledger window, budgets, stage) × turnOutcome → verdict, per the §1 table.

### 4. Progress ledger (two signals, not one counter)

Each continuation folds `TurnOutcome{actionFingerprint, observationClass, observationHash, stateDigest, mutated}`.

- `actionFingerprint`: `tool + normalized args` (normalization specified per tool in the spec appendix at implementation time: paths canonicalized, volatile flags stripped, timestamps/nonces excluded; raw tool-name-only or raw-args fingerprints are both rejected — the former false-fires on refining greps, the latter is evaded by trivial arg changes).
- `observationClass`: `ok | empty | error | timeout | approval-pending | external-unchanged`. `observationHash` inputs are canonicalized (timestamps, request ids, RNG redacted) over a stated scope; unhashable/noisy observations fall back to class-only comparison (never "novel by default").
- `stateDigest` scope is fixed and stated: job phases + delegate phases + watch conditions + goal wait predicates + worktree file-listing digest (names + sizes + mtimes, not full contents). History-covering digests are forbidden (they always change and would deaden the breaker).
- Live signals: observation-novelty (canonicalized hash unseen in last N=8), state-digest delta, subgoal evidence (a registered condition flipping true — v2 direction 1 in its cheapest form).
- Stall signature (keeps the shipped two-tier strictness — the I7 regression is explicitly not taken): K consecutive turns with identical `(actionFingerprint, observationClass)` **and** no digest delta, with K=3 after first mutation-or-subgoal-evidence, K=6 while never-advanced (read-heavy openings are never penalized). `mutated` is a first-class input: a mutating turn with a digest delta resets both tiers; a mutating turn with no digest delta (junk write) still accrues repetition.
- Total backstop (the I2 fix): B consecutive non-advancing turns (no novelty, no digest delta, no subgoal evidence — regardless of alternation) with B=12 default also graduates to block. Period-2 alternation and timestamp-novelty evasion therefore cannot run unbounded.

### 5. Accrual and budgets (the unbounded-path closure)

- `budgets{maxContinuations (default 200, cap 1000), deadline (default 4h wall-clock from SetGoal, cap 24h), maxParkedTotal (default 24h)}`. All three have shipped values in a constants table (the `goal.go:25-29` precedent) — no valueless mechanism.
- The wall-clock `deadline` **runs** across waiting (frozen deadlines would let re-registration park forever). Wake, evaluation, nudge, and notification-driven turns all count toward `maxContinuations` (no free turns at expiry rate). Parked time accrues toward `maxParkedTotal`.
- Re-park bound: at most 3 consecutive auto-re-parks (stage-2 auto-waits or model re-registers without intervening advancement); the 4th consecutive stall graduates to `block`. Already-true predicates at registration do not park: attach-scan-true drives the evaluation turn immediately with a 30s cooldown before any same-predicate re-park, closing the tight park/wake loop.
- Deadline expiry outcome is specified: the goal is **not** auto-blocked silently — the gate drives one final evaluation turn carrying `"deadline exceeded"` + the wait labels, delivers the honest notice, and then blocks with verdict `"deadline exceeded"` (distinct from `"no progress"` and `"budget exhausted"`, fixing the I10 collapse). `maxContinuations` exhaustion blocks with `"budget exhausted"`.
- `/goal resume`: from a `"no progress"` block → ledger reset, waits cleared, budgets kept. From a budget/deadline block → rejected naming the exhausted budget unless the caller supplies a renewal (`/goal resume --extend <budget>`), which is the only path that extends budgets. Resume never silently re-blocks on the same check without consuming a turn that says why.

### 6. Graduation, watchdog, stop-claim verification

- Stages: (1) steering nudge naming the repetition evidence ("3 turns of `<fingerprint>` with no state change; state your unblock condition or register the wait"); (2) park-until-wake with a bounded auto-wait when the stall looks like waiting (external-unchanged class, pending asks, live-but-unwaited dependents); (3) terminal block with the correct distinct verdict + exactly one steering transcript note (today's breaker-note behavior).
- Quiet-goal watchdog (v2 direction 2, per-stretch not per-window): one owner notice per park stretch (reset on wake/activity), plus one for active-but-quiet past the quiet threshold (default 30m, configurable). Delivery channel is a named `EventGoalWatchdog` announcement (never a model turn — parked goals run zero turns). "Quiet" is defined: no ledger change, no wake, no owner-visible output since stretch start; the wake itself resets the stretch. A 24h park therefore notifies at most twice (park-start + optional single reminder at half-deadline), never ~144 pings.
- `update_goal("complete")` verifies **iff** the goal carries registered conditions (a condition-registration path distinct from waits; minimal v1 shape: `goal_expect(desc, predicate)` or conditions attached at `goal_wait`/`SetGoal` time). Goals without conditions keep the v1 self-declare + evidence-audit prompt path unchanged (the haiku goal completes). With conditions: the verifier evaluates the named conditions and rejects with the failing condition named; cost scales with the claim (v2 direction 3). The condition-query schema is defined in the implementation plan before any verification code.
- Continuations carry deltas (v2 direction 4): condition flips + new terminal events/output since last evaluation, not full state.

### 7. Persistence, projection, tools, timers, restart

- `schema.GoalSnapshot` v2: `{objective, status, waits[{wait_id, kind, predicate (full payload: target ids, baselines, matcher, approval content key, child id), label, since, deadline, fired_epoch}], ledgerSummary{window hashes, repetition count, tier}, budgets{maxContinuations, usedContinuations, deadline, parkedTotal}, stopReason, autoReparks}`. `SessionMeta.Goal` persists it; restore re-validates every predicate immediately (attach-scan at restore).
- Terminals are **never** restored as terminal (keeps `TestGoalRestoreOnlyActive` green: re-restoring would re-emit `EventGoalEnded` since `reported` is runtime-only and resets on load). Migration table (bound-preserving): old `{streak, madeProgressOnce}` → initial ledger window that preserves *remaining-till-block*, not full history: `remaining = (madeProgressOnce ? 3 : 6) - streak`; seed `K - remaining` synthetic identical non-advancing entries (clamped ≥0), so a streak-5 goal blocks after exactly 1 more non-advancing turn, and a streak-0 goal gets the full new-tier allowance. One-way, tested.
- Restored active goals stay "loaded but idle" (`session_state.go:327-329`); restored `waiting` goals re-arm one coalesced `sclock` timer from the earliest wait deadline (never 8 independent timers) and deliver pending loss notices at the next turn tail. Restarted-away predicate substrate (job gone, ask set rebuilt without the content key) yields the honest loss notice, never a silent strand, never a duplicate terminal report.
- Appwire: `EvenerThread.Goal` gains `waiting_on[] + nearest_deadline + progress{usedContinuations, maxContinuations}` (machine-readable, `LangGraph-interrupted`-shaped); `GoalStateData` gains the wait list (fixing the M2 wire gap); new `EventGoalWaiting / EventGoalResumed / EventGoalWatchdog` project to announcements; terminal report stays on every stop path. Parked-state surfacing: chip aggregates to `"waiting on <n> · <nearest label> · <deadline>"`; `WireState`/autonomy treatment marks timer-parked goals as autonomy-in-flight (never "needs you" with nothing to answer); the notifying turn for a waited target **is** the wake turn (no double-turn accounting).
- Tools (capability-gated as today): `goal_wait(kind, predicate, timeout)`, `goal_cancel_wait(wait_id)`, `goal_expect(desc, predicate)` (condition registration for verification), `/goal resume [--extend]` alongside `update_goal`. Registration validation errors name the failed check.
- Compaction re-injects objective + active wait labels + ledger digest in delta shape (bounded).

### 8. Child sessions

The child gap is closed by mechanism, not assertion: a child persists its wait record in its own store; the parent→child forward is specified — parent notification matching a child's `until_child` or a forwarded predicate re-checks the named child's waits under the §3 lock order (child registry → child goalUpdateMu; parent locks never held across the child claim) and drives the child's wake turn through the existing delegate-drive seam (`driveChildIfNotStopGated` path). Cross-session predicate reads (child querying parent job/delegate state) take parent controller/job-manager read locks before the child claim, in the §3 order. If the forward cannot be built in the first slice, `until_child` from a child is rejected at registration with "child waits scoped out in this slice" — an honest, tested boundary, not a silent strand.

### 9. Testing and rollout

- Pure tables: `decideGoalStep` over (status × waits-truth × ledger × budgets × stage); repetition/novelty rules over canonicalized pairs; migration table bound-preservation.
- Registry: validation reject cases (hallucinated job/delegate/file/ask/URL/child), expiry re-drives once, idempotent re-register, max-8, coalescing, overflow → end-notice, re-registration abuse (10m-loop-forever test must block via re-park bound), already-true attach-scan (no tight loop), double-fire race (notification + timer same ms → exactly one wake), crash-between-consume-and-kick (re-drive on next tail).
- Gate: park skips every fold; expiry/expiry-evaluation accounting; stale-park kicks once; retarget voids waits + disarms timers; restarted-wait re-validation; prose-goal completion (no conditions → self-declare path); conditioned-goal rejection naming the failing condition; read-heavy opening (6 similar reads, no digest delta → no block); alternating-poll backstop (period-2 → B=12 block); timestamp-noisy output (canonicalized → still stalls); genuine-retry (same test 3× with digest delta → live).
- Persistence: round-trip with full predicate payloads; restart-during-wait (timer re-arm, loss notice, no duplicate terminal); v1 migration (streak-5 → 1 more turn; terminals dropped).
- Concurrency: `-race` hammering SetGoal/ClearGoal/`goal_wait`/`goal_cancel_wait`/gate/timer-callback; lock-order assertion.
- E2E (scripted adapter, no live provider): poll-then-event-arrives (zero extra LLM calls while parked); true loop (K=3 → nudge → park → block, exactly one transcript note); notification re-arm; watchdog per-stretch (24h park → ≤2 notices).
- Rollout: slice 1 = registry + park + persistence + timers + validation + atomic fire (§§1–3 partial, §§6–7 partial — the findings show the "easy first slice" still needs the I1/I3/I8 fixes inside it); slice 2 = ledger + backstop (§4); slice 3 = graduation + watchdog + verification (§6 claim path).

## Appendix A — adversarial disposition

- A-I1 (gate transitions/verdict mapping) → §§1, 3 (transient clause, atomic step, truth table with ties).
- A-I2 / B-I1 / B-I10 (zero-accrual bypass, re-park livelock, budget defaults/verdict/resume) → §5 in full (deadline runs, wake turns count, parked-total cap, re-park bound 3, cooldown, defaults table, three distinct verdicts, resume renewal rule).
- A-I3 / B-I3 (approval durability; predicate persistence/validation) → §2 (content-key approval, full predicate payload persisted, fail-closed registration validation).
- A-I4 / B-I5 (child driver) → §8 (forward path + lock order, or honest scoped rejection).
- A-I5 / B-I6 (verification without conditions) → §6 (verify iff conditioned; `goal_expect`; schema before code).
- A-I6 / B-I8 (fire atomicity, generations, lock order, timer disarm) → §§2, 3 (`claimWaitFireLocked`, `fired_epoch`, crash recovery, full lock-order table, disarm on clear/retarget).
- A-I7 / B-I2 (two-tier regression; weaker stall rule) → §4 (K=3/K=6 kept, fingerprint normalization, canonicalized hashing, fixed digest scope, `mutated` in rule, B=12 total backstop).
- A-M1 / B-§7-restart (timer re-arm, "never" softened) → §7 (coalesced `sclock` timer, notices at next tail).
- A-M2 / B-I4 (migration truthfulness, terminal restore) → §7 (terminals stay dropped; remaining-till-block seeding table).
- A-M3 (external predicate scope, timer composition) → §§2, 7 (eval scoping, coalesced timer, exhaustion = honest notice).
- A-M4 (resume into instant re-block) → §5 (renewal rule).
- B-I7 (watchdog cadence/channel/definition) → §6 (per-stretch, `EventGoalWatchdog`, defined quiet/reset, ≤2 notices per 24h park).
- B-M1 (purity) → §1 (`predicateTruth[]` seam). B-M2 (surfacing) → §7 (aggregation, wire list, autonomy treatment, wake-turn accounting).

## Appendix B — constraints carried from v1 (do not regress)

- Terminal report on every stop path, exactly once (`TakeTerminalReport` semantics kept).
- Goal state durable across resume; restored actives "loaded but idle" (no auto-kick).
- Capability gating on all goal/ask surfaces.
- Lock discipline: predicate compute before `goalUpdateMu`/`s.mu`; `sclock` for all wait stamps.
- No unbounded goal (v2 note constraint) — enforced by §5, tested by the abuse cases in §9.
