# W5 close (T6, pre-merge) — task status report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w5-interaction` · **HEAD before task** `5d25dd893` · **commit range** `5d25dd893..HEAD`
(item 0 `4e3518433` + this report/artifacts commit).
**No merge performed** (controller owns the serial W5→integration→W7 merges).

## What was done

- **Item 0 — steer copy.** Renamed the QueueStrip drain button `"Steer now"` → `"Steer queue now"`
  (Jesse-approved). RED-first: drain matchers switched to the new exact name → witnessed RED → label
  renamed → GREEN → the negative-lookahead trap the brief warned about was witnessed (the old
  `/^steer(?!\s*now\b)/i` matched BOTH labels post-rename) and fixed. **Trap handled with a correction
  to the brief's literal suggestion:** the composer's own Steer button's real accessible name is
  `"Steer Shift+Enter"` (it carries the Shift+Enter KeyHint — verified empirically AND live), so
  `{ name: "Steer" }` exact would NOT have matched; used exact `"Steer queue now"` for the drain and a
  readable predicate (`startsWith("Steer") && !includes("queue")`) for the composer control. All
  matchers across Composer / QueueStrip / integration updated; repo-wide `src` sweep for "steer now" is
  clean. Commit `4e3518433`.
- **Item 1 — parity sweep.** Both floors + the chrome bullets swept exhaustively (4 parallel research
  packages + controller verification of the load-bearing gaps). Full record:
  `w5-close-t6-parity-sweep.md`; compact summary in the wave report.
- **Item 2 — live proof.** Real isolated hub + real `serf serve` daemon + real `oai-work/gpt-5.4-mini`
  →`gpt-5-nano` session, driven via Chrome. Evidence: `w5-close-t6-evidence/` (10 screenshots).
- **Item 3 — gates.** `tsc`→0, `vitest`→0 (136 files / 2034 tests, baseline-identical), `lint`→0,
  `build`→0 (dist/PLACEHOLDER restored). No Go changes.
- **Item 4 — wave5-report** written to `docs/superpowers/plans/wave5-report.md`.

## Gate summary
`tsc --noEmit`=0 · `vitest run`=0 (136 files / 2034 tests) · `lint`=0 · `build`=0 (PLACEHOLDER restored, tree clean).

## Concerns (for Jesse / M9 / M10)

1. **Parity — two HIGH gaps** (verified against source):
   - Denied/errored `ask_user` still renders an interactive answerable card (`deriveAskQuestions.ts:39-41`
     has no `item.error` check; projector stamps `Status:"completed"` on error at
     `appwire_projection.go:437`). Fix belongs with the absorb roster's `ItemModel.error` consumption.
   - Sandbox-escalation resolve doesn't treat a Conflict as terminal (`threads.ts:850-854` drops
     `mapConflict`) — a resolved-elsewhere escalation is retryable-forever until reconnect.
   Plus a cluster of MEDIUM gaps (send/steer/drain optimistic chips unrendered; model-switch not
   busy-gated; picker not Escape/outside-click dismissable; DEFAULT_EFFORT_LEVELS fallback dropped;
   ask-transcript re-architecture with no `[data-ask-anchor]`/`.ask-settled-line` — a documented
   wave-4 choice needing M9/M10 ratification; location cluster absent; OS-notification loudScope not
   ported; "/" palette M6). Full list + LOW items in the sweep artifact.
2. **Live proof — one journey partial.** Journeys 2/3/5/6/7 passed live (ask round-trip with verified
   lockout + both aria-live strings; model switch; goal set→active→clear; tasks 0/5 + rows; base64 PNG
   paste round-trip incl. the model reading the image colors). Journey 4's Session-actions menu is
   confirmed (fork/aside busy-gated during active). **Journey 1 (queue/steer/edit/promote under load)
   could not be exercised live** — the bare `serf serve` daemon advertises interrupt/steer/queue
   capability = false (hub-spawn is Wave 6, unavailable), so no queue can form. The composer gates its
   controls correctly per the absent capabilities (not a UI defect); these behaviors are covered by the
   green unit+integration suites and the parity sweep. The composer's own `"Steer Shift+Enter"` control
   was confirmed present live.
3. **Coordination finding.** serf-hub enforces a **host-global flock at `$HOME/.serf/hub.lock`**
   (single hub per host). The parallel W7 close's live-proof hub held it, so I ran a fully isolated hub
   under a fake `HOME` (own lock/rendezvous/state, port 19281) — W7 untouched and verified unaffected.
   Two parallel closes contend on this flock; worth noting for future parallel live-proof scheduling.

## Not done here (controller's next steps)
The absorb roster (types.gen.ts regen; shellTool consumes typed `exitCode` + `ItemModel.error`;
reconcile 3 stale rendering comments; escalation-`resolved` reducer case) — and the ask-card
`item.error` fix belongs with it — then the serial merges: **W5 → integration first, W7 second.**
