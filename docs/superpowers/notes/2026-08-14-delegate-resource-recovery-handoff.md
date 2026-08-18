# Delegate resource recovery handoff

Date: 2026-08-14

> **HISTORICAL — do not act on this checkpoint.** The work it hands off is
> complete and landed on `main` (kata `my73`, as the linear commit series
> `3507688cf` through `554221673`; there is no merge commit). Every ref below is
> stale: the `delegate-resource-task6-clean` branch and worktree no longer
> exist, and `2da986339` does not exist in this repository — citing it as a
> "required merge base" is what caused a fourteen-kata misattribution, seven of
> which were already fixed on the line nobody checked (kata `krgs`). Read
> `docs/subagent-management/11-delegate-resource-model.md` for the shipped
> contract instead.

## Checkpoint

- Worktree: `/Users/jesse/prime-radiant/toil-suite/evener/.worktrees/delegate-resource-task6-clean`
- Branch: `wip/delegate-resource-task6-clean`
- Task 7 code closure: `521a4892d977927154f34636343d84e8dda15508`
- Documentation/handoff baseline: `d6db1ad730e793f36d4594000df5ba5e8ef97e27`
- Task 5 foundation / required merge base: `2da9863390e3e064fc015afe79a54fe8a8ce1d8f`
- State at this checkpoint: tracked worktree and index clean.
- Rebase, merge, and push: none performed. This branch is not landed.

Derive the actual resume HEAD instead of copying a stale hash: set
`resume_head=$(git rev-parse HEAD)`, verify the expected branch, then require
both `git merge-base --is-ancestor 521a4892d977927154f34636343d84e8dda15508 "$resume_head"`
and `git merge-base --is-ancestor d6db1ad730e793f36d4594000df5ba5e8ef97e27 "$resume_head"`.
This proves the exact checkout being resumed contains both the completed Task 7
code closure and the original tracked handoff before any Task 8 work begins.

This is an intentionally nondeployable flag-day implementation checkpoint. Do
not merge, release, or run it against durable user state until Tasks 8–14 and
their final integration proof are complete.

## What is complete

Task 6 is complete at `5df4ad5f487f3674f4016f40eeb82d3cf49b7aa4`: root
bootstrap, fail-closed legacy scans, stable create/restore, committed
descriptor construction, and the registered create boundary. Task 7 is complete
at `521a4892d977927154f34636343d84e8dda15508`: registered running and idle
send, cold restore, caller-delivery durability, canonical terminal packets,
supervision, quiet attention, and root-owned stop reconciliation. Its final
capability correction preserves nonblocking generic `job_stop`, makes owned
shell cleanup errors observable, and retains observer/root-close behavior.

The immutable code checkpoints above are committed evidence. The ignored Task 6
and final Task 7 SDD reports are not committed or staged, but record behavioral
REDs, independent reviews, and final-head normal/race, store, affected-family,
deterministic fuzz, registry, documentation, formatting, ancestry, diff, and
clean-state gates. This note does not repeat their timings or full execution
log. Task 6's report retains one process-evidence concern: the first six
stabilization commits lack preserved contemporaneous cached-diff review
evidence, although retrospective range checks passed; Task 7 has no known open
defect at its final checkpoint.

## Binding product decisions

- Agent-facing `delegate` creation neither exposes nor accepts `max_wait_ms`.
  `delegate_send` and `job_stop` retain their existing wait semantics.
- There is no public control for the maximum result size. Bounded post-commit
  create results retain the stable identity and omit only optional diagnostics
  in the documented order.
- This is a flag-day cutover: no migration, compatibility alias/window, mixed
  loader, dual writer, fallback route, or feature flag.

The six approved cuts are exactly:

1. No public activation-addressable status, output, history, watch, stop, or UI rows.
2. Legacy delegate JobRecord state fails closed as `legacy_delegate_state`.
3. Legacy delegate-job-addressed watch rows fail closed as `legacy_delegate_watch_state`.
4. `job_stop(dlg_...)` is always recursive; `include_children` cannot weaken it.
5. `job_status(dlg_...)` is metadata-only and does not acknowledge terminal delivery.
6. No autonomous or time-based idle-runtime unload lifecycle protocol.

Do not mistake preserved behavior for a removal target: stable watch
sources/receivers, `watch_parent`, observer sidecars and callbacks, quiet
supervision, hooks, auto-nudge, salvage guidance, attention escalation,
delegate-owned shell behavior, `max_retained_terminal` demand reclamation,
and terminal/result/worktree fidelity must survive the cutover.

## Remaining work and ordering

Tasks 8–14 remain open: stable attention/watch/observer/shell delivery;
retention/stop/worktree/close; stable tools and read-only projections;
AppWire/Hub/TUI/web cutover; legacy retirement; shipped user-facing docs and
prompts; then final recovery verification.

Treat production work as sequential through Task 12 because the flag-day
authority and typed endpoint contracts cross those tasks. Independent read-only
review, test-design, or consumer inventory work may run in parallel when it
does not edit shared files or preempt the owning task. Task 13 waits for the
implemented user-facing contract, and Task 14 waits for every prior task. Do
not inspect, copy, clean, reset, or repurpose the abandoned
`/Users/jesse/prime-radiant/toil-suite/evener/.worktrees/delegate-resource-recovery-design`
worktree. Do not start Task 8 by copying or inspecting the separate abandoned
`delegate-identity-integration` worktree either; both are evidence only, never
implementation bases.

## Safe resume

1. Run literal `true`; stop immediately on actual `Too many open files (os error 24)`.
2. Verify this exact worktree, branch, expected prior commit, merge base, and
   clean tracked porcelain/index before any change. Do not fetch, rebase,
   merge, cherry-pick, reset, clean, stash, switch branches, or push.
3. Read `AGENTS.md`, `docs/testing.md`, the evergreen model, this dated plan,
   then the ignored SDD progress and owning task report before implementing the
   next task. Use the evergreen model for product authority and the dated plan
   for task execution.
4. Keep Kata `my73` and the SDD ledger as the project record. There is no
   Linear work for this project.
5. Establish a deterministic behavioral RED against the applicable unchanged
   boundary, make the smallest coherent fix, run the owning task's gates and
   independent review, and record evidence before advancing.

## Authoritative pointers

- Product authority: `docs/subagent-management/11-delegate-resource-model.md`
- Execution plan and remaining task dependencies: `docs/superpowers/plans/2026-08-12-delegate-resource-recovery.md`
- Task 6 closure: `.superpowers/sdd/2026-08-12-delegate-resource-recovery/task-6-stabilization-report.md`
- Task 7 closure: `.superpowers/sdd/2026-08-12-delegate-resource-recovery/task-7-report.md`
- Append-only project ledger: `.superpowers/sdd/2026-08-12-delegate-resource-recovery/progress.md`
