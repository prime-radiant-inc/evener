# Recursion validation-tail fixes

**Date:** 2026-06-13 · **Branch:** `job-control-spec` (HEAD `4c087ccd`) ·
**Status:** decisions made (below); ready to execute the fix pass. Never push;
Jesse merges.

## Decisions (Jesse, 2026-06-13)

- **A7** → **steer into the drive turn.** Treat `sub.driving` like `sub.running`:
  inject the input into the in-flight drive turn (the existing
  send-to-running-subagent path); the subagent's model sees its queued
  notifications + the steered input in one turn. No concurrent turn.
- **F1** → **text-only, no code change.** Granting a `delegation_allowance` to a
  role that lacks the `delegate` tool is ACCEPTABLE — it's a harmless no-op for a
  non-delegating role; do NOT reject at grant time. Fix is documentation only:
  clarify that a role needs the delegate tool to actually use its allowance. F1
  is therefore NOT a correctness blocker.

## Context

The recursive-subagents implementation (T1–18) passed per-task adversarial
review and a live coordinator e2e run (4/4, owner-scoping verified against
transcripts). Two **whole-campaign** validation passes then ran:

- **roborev sweep** — codex auto-reviewed all 16 campaign commits; findings
  triaged (13 "real", 3 already-addressed, 6 nitpick, 1 false-positive).
- **Usability capstone** — gpt-5.5 drove fresh *unscripted* recursion sessions;
  the surface text was critiqued for clarity.

These caught **cross-cutting** bugs the per-task reviews structurally could not
(each reviewer was scoped to one task): restore-path threading, a fallback
ordering bug, a drive-turn concurrency guard, and an allowance↔role-tools
decouple. **A1 is a release blocker — the branch is not mergeable until A1, A2,
A4, and A7 are fixed** (F1 is a text-only clarification per the decision above,
not a correctness blocker).

Each fix is TDD red-first, gets a scoped spec-review + orchestrator grep-verify +
gate, and the whole-campaign sweep re-runs after the pass.

---

## Must-fix (verified against the code at HEAD)

### A1 [BLOCKER] — restored root loses `delegationAllowance`
- **Symptom:** a resumed root session has `delegationAllowance == 0`. The
  delegate gate (`prepareSubagentRun`, `allowance <= 0 → reject`) then blocks
  **all** delegate spawns — even ordinary non-recursion leaf delegates. Breaks
  the shipped `delegate` feature for every resumed session.
- **Root cause:** `RestoreSessionFromMetaWithConfig` (`agent/session_init.go:317`)
  sets `delegationAllowance: cfg.spawn.delegationAllowance` unconditionally.
  `NewSession` (`:99-103`) derives a root's allowance from `cfg.MaxSubagentDepth`
  via the `if cfg.spawn.parentSessionID == ""` guard; the restore path has no
  such guard and `cfg.spawn.delegationAllowance` is 0 for a root.
- **Fix:** apply NewSession's root-derivation in the restore path (root →
  `cfg.MaxSubagentDepth`; child → the carrier, which for a restored delegate
  comes from the descriptor). Mirror NewSession exactly.
- **Test (red):** restore a root whose config has `MaxSubagentDepth=N`; assert
  `restored.delegationAllowance == N` and that it can spawn a delegate. (Today: 0,
  rejected.)

### A2 [HIGH] — restore omits `treeCounter` → cap bypassed after restart
- **Symptom:** after a process restart, a restored root (and its resumed delegate
  children) have `treeCounter == nil`; `reserveTreeSlot` treats nil as unbounded,
  so the 16-delegate tree cap is inoperative.
- **Root cause:** the restore `Session` struct literal (`agent/session_init.go:311-340`)
  has no `treeCounter` and no `tc := newTreeCounter(); cfg.spawn.treeCounter = tc`
  block (NewSession has it at `:104-113`). The delegate-restore spawnConfig
  (`agent/job_delegate.go:~579`) omits `treeCounter: s.treeCounter`.
- **Fix:** mint a fresh counter for a restored root (rebuild from zero per spec
  §4), mirror onto `cfg.spawn.treeCounter` + `s.treeCounter` like NewSession, and
  thread `s.treeCounter` into the delegate-restore spawnConfig so resumed children
  share it.
- **Test (red):** restore a root, spawn 16 concurrent delegates, assert the 17th
  gets `tree_at_capacity` (today: unbounded); a resumed delegate child reserves
  on the same counter.

### A4 [HIGH] — unreachable-child fallback drops its own notification
- **Symptom:** when a non-resumable child's pending attention should escalate to
  the parent (`"child unreachable:"`, T15 fallback), the notification is silently
  dropped. The fallback is non-functional.
- **Root cause:** `renderUnreachableChildPendings` (`agent/job_watch.go:2680-2690`)
  calls `s.enqueueJobNotification(n)` then **immediately** appends
  `EventJobNotificationDelivered` for the same record → `NotifyDelivered`. When
  `filterDeliverableJobNotifications` (`agent/session_lifecycle.go:920`) drains the
  queue, `ShouldDeliver(rec)` is false → `n` is filtered out.
- **Fix:** do not mark the record delivered before it is actually rendered.
  Settle the forwarded copy only after the turn renders it (or let the normal
  delivery path mark it). Match the non-fallback settle ordering.
- **Test (red):** a non-resumable child with a pending forwarded terminal →
  drive boundary → assert the parent's model receives the `"child unreachable:"`
  notification. Prove non-vacuous.

### A7 [MED-HIGH] — concurrent turn on a mid-drive child (data race)
- **Symptom:** a `job_send_message`/watch resume arriving while a drive turn is
  in flight (`sub.driving == true`, `sub.running == false`) launches a second
  concurrent `ProcessInputKind` on the same child session — concurrent mutation
  of session history/state with no synchronization.
- **Root cause:** `startOrSteerSubagentRun` (`agent/subagents.go:631`) guards
  `if sub.running` but not `sub.driving`. `driveSubagentNotificationTurn` checks
  `driving`; the two entry points are not mutually exclusive on it.
- **Fix (decided — steer into the drive turn):** make `startOrSteerSubagentRun`
  driving-aware — treat `sub.driving` like `sub.running` and STEER the input into
  the in-flight drive turn (the existing send-to-running-subagent path). The
  subagent's model sees its queued notifications + the steered input in one turn;
  no concurrent turn starts.
- **Test (red, `-race`):** launch a drive turn, concurrently call
  `startOrSteerSubagentRun`; assert no second `ProcessInputKind` starts
  concurrently (no data race).

### F1 [USABILITY, text-only] — allowance>0 on a non-delegating role silently accepted
- **Symptom:** `delegate(agent_type:"subagent", delegation_allowance:1)` is
  accepted (1 < own), but the built-in `subagent` role has no `delegate` tool, so
  the child can't delegate — it only surfaces as a dead worker round-trip. The
  model's natural first move ("subagent" for a subagent) is the wrong choice with
  no warning. (Found independently by the usability pass; the only first-try
  stumble in the whole assessment.)
- **Root cause:** `baseSubagentToolPolicy` (`agent/subagents.go:170-184`) only
  consults `canDelegate` for the untyped/default child; a typed role returns its
  tool list verbatim, ignoring allowance. Allowance and tool-set are decoupled.
- **Decision (Jesse):** allowance-without-the-`delegate`-tool is ACCEPTABLE — a
  harmless no-op for a non-delegating role. Do NOT reject at grant time.
- **Fix (text-only):** `agent/internal/tool/definitions.go:127`
  `delegation_allowance` description — add a caveat that the allowance only takes
  effect if the delegate's role actually has the `delegate` tool (the built-in
  `subagent` role is a non-delegating leaf); `agent/agents/subagent.md:3` —
  front-load "cannot delegate regardless of any allowance; for a multi-level tree,
  delegate with the default role." No behavioral change; a quick comprehension
  read of the reworded text suffices (no new code test).

---

## Verify-and-fix (roborev claims, not yet code-verified — each fix's red test IS the verification)

- **A3** cascade fires on already-terminal delegate
  (`agent/session_tools_jobs.go:531`): `delegateChildSessionToCascade` doesn't
  check `rec.Status.IsTerminal()`; `job_stop` on a `completed` coordinator may
  still cascade. If real, gate the cascade on non-terminal.
- **A5** stale forwarded pending when a child self-delivers
  (`agent/jobs_nested.go:642` region): a child completing mid-own-run leaves the
  parent's forwarded copy `NotifyPending` → later false `"child unreachable:"`.
  If real, settle on self-delivery too.
- **A6** notification arriving mid-drive while the parent is idle
  (`agent/subagents.go:717-728`): drive returns false, no reschedule, parent idle
  → stuck. If real, reschedule/notify after the drive turn ends.
- **A8** depth-≥2 closed-store fallback (`agent/session_tools_jobs.go:323`):
  `readSession = ownerSess` makes `jobReadClosedStoreFallback`'s `local ==
  current`, so the ancestor's forwarded copy is never consulted.
- **A9** `collectDescendantJobs` swallows store errors
  (`agent/jobs_nested.go:103-116`): bare returns; a depth-0 store error makes
  `job_list(include_descendants)` return an empty list with 200, regressing from
  plain `job_list`. If real, surface the depth-0 error.
- **A10** `Depth int json:"depth"` has no `omitempty` (`session_tools_jobs.go:642`)
  → `"depth":0` on every `job_list` (and the struct comment claims it's omitted —
  factually wrong). Fix `omitempty` + comment.
- **A11** descendant `runtime_lost` resumability mis-projected in
  `include_descendants` (`agent/jobs_nested.go:86`): `projectJobRecord(s, ...)`
  uses the root session for all rows (the T11 advisory T12 fixed for *reads*; the
  *list* projection still uses root). Project via owner.
- **A12** e2e card assertions imprecise (`recursion-coordinator-fanout.md`,
  `recursion-deaf-coordinator-drivedown.md`): owner-scoped wording (frame
  `job_id=` subject vs substring) + cascade-stop on a COORD that completes
  naturally. **Not a product bug** — the live run PASSED and the orchestrator
  verified owner-scoping; this is card robustness. Tighten the assertions so a
  naive future runner won't false-fail.

---

## Dismissed (no action)

- **Already addressed (B):** T9 fake-green test (fixed `fad107f2`), T10
  double-fault arm (fixed `994c62b9`), `job_send_message` ownership gate.
- **Nitpick (C):** documented double-fault-stranded-pending tradeoff,
  `include_children`+cascade double-stop (disjoint job sets), e2e ledger phrasing.
- **False positive (D):** the freeze-validation allowance-guard claim.

## Carry-forward cleanup (behavior-neutral, one commit — tracked as #21)

Stale "dormant" comments (`tree_counter.go`, `session_config.go`, `session.go` —
counter is live now); `stopDelegateSubtree`'s unused receiver; `sub.sess` read
after `sub.mu` unlock (`liveSubagentSessions`/`liveDirectSubagents`);
`finalizeKeptSync` shell-only comment; the dead `shellArgs.Background` field
(rewrite the close-during-start test against the promotion path).

## Sequencing

1. **A1 + A2** (restore-path cluster — same area) — BLOCKER, do first.
2. **A4 + A7** (drive-down edge cluster — same machinery).
3. **F1** (text-only: clarify the `delegation_allowance` description + `subagent`
   role text; no code change).
4. **A3 / A5 / A6 / A8 / A9 / A10 / A11** — verify-and-fix sweep (TDD each).
5. **A12** — tighten the two cards.
6. Re-run the affected e2e cards live + the full gate (`make test && make lint &&
   go test ./... -race`).
7. Cleanup batch (#21).
8. Handoff for Jesse to merge (never push).
