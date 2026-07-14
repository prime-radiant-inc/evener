# Delegate-lane disposal — §P2 completion-nudge eval gate

These three scenario cards are the **live eval gate** for the completion nudge
(`DisposalHint`) of the auto-delegate-lane-disposal feature (spec
`docs/superpowers/plans/2026-07-13-auto-delegate-lane-disposal.md`, §P2). The
nudge is the primary path by which a model retires a finished delegate's
worktree, so its effectiveness is graded behaviorally against real providers —
unit tests cannot judge whether a model *acts* on the copy.

The cards:

- `dispose-after-merge.md` — a delegate's work is ancestry-merged; the model
  should dispose its lane.
- `dispose-after-squash-merge.md` — the work is squash-merged (not
  ancestry-visible); `op=dispose` refuses, and the model must either verify the
  squash landed and `force`, or report the situation. Silent abandonment fails.
- `no-early-dispose-resume-later.md` — the scenario keeps resuming the delegate;
  the model must NOT dispose or force it early.

## The gate (falsifiable)

Run **3 providers × 3 runs each** (9 runs per card). Thresholds:

- **(a) dispose-after-merge**: ≥ 2/3 runs per provider dispose the delegate
  after the merge step — including the squash-merge card, where a pass is a
  verified `force` OR an honest report (silent abandonment is a fail).
- **(b) no early dispose**: 0/9 runs dispose or `force` a delegate the scenario
  later resumes.
- **(c) no scolds**: 0/9 runs express confusion about the nudge or refuse to
  proceed.

The §P2 copy iterates until the gate passes; record each run's verdict
alongside these cards (a short results table per provider/run) when the gate is
executed.

## Where the LIVE run fits

**The live 3×3 run is a human-triggered step, NOT part of CI.** These cards are
executed by an agent (Claude / Codex / etc.) against a freshly built `serf`
binary with real, billed provider calls — see `test/scenarios/README.md` and
`docs/agentic-testing.md` for the harness conventions. CI covers the mechanics
deterministically (the `TestE2E_*` session-level integration tests in
`agent/dld_e2e_test.go`, plus the P0/P1/P3 unit suites); the behavioral nudge
gate is run on demand before shipping copy changes, not on every push.
