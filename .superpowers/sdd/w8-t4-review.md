# Wave 8 — T4 (session chrome + optimistic-pending chips) — adversarial review

**Reviewer verdict:** APPROVED (2 Minor report-accuracy findings; no Critical, no Important).
**Range:** `e3b9c188c..08e1cfa5b` (branch `w8-chrome`). Worktree `webui-w8-chrome`.
**Method:** spec/pin compliance + code quality, every load-bearing claim verified against the Go
wire sources and the TS, gates re-run from scratch, two headline guards mutation-tested against
primary source (transient edit → run → `git restore`, tree left pristine, HEAD unchanged).

## Gate summary (re-run by the reviewer, AND-chained, honest exit codes)

`tsc --noEmit` exit 0 · `vitest run` exit 0 — **223 files / 3236 tests passed** · `biome ci src`
exit 0 (626 files) · `npm run build` exit 0, `dist/PLACEHOLDER` restored, **tree clean**. HEAD count
3236 independently confirmed; `StatusRow.test.tsx` (19) and `statusFormat.test.ts` (12) counts
cross-checked against vitest's own per-file totals. No new test files (only new non-test files are
the report `.md` and `pendingchips.module.css`) — consistent with the file count holding at 223 and
with the diff.

## Probe outcomes

- **P0 (PIN obligations):** all obligations naming T4 MET. Binding constraints — chokepoints
  untouched, `reducer.ts`/`tokens.css` untouched (git-diff-verified), design-system binds satisfied,
  wire-truth fixtures, gates green. Pins — DEFAULT_EFFORT_LEVELS **MET** (traced), method-filter
  **MET**, PIN-B **MET** (Combobox kept, fixes stand alone), PIN-E **MET in spirit** (see note N1).
  Dispatch deltas (1)–(4) all satisfied (popout: `popOutPane` never referenced in T4 files).
- **P1 (epoch-clock):** root-cause claim **verified true** — `epochMsToISO` (reducer.ts:78-80) guards
  only `undefined`, so a present-zero `activeTurnStartedAt` becomes `"1970-01-01T…"` and clocks
  now-minus-epoch. Fix (statusFormat.ts:60-62, reject `!isFinite || startedMs<=0` → banked total) is
  correct; the wire-true fixture drives the REAL `hydrateThread`; mutation removing the guard fails 3
  tests (reviewer-confirmed). Recommendation below.
- **P2 (reasoning-effort honesty — KEY):** the wire does **NOT** distinguish "none" from absence —
  both mean provider-default. Proven by three Go sources (see below). Collapsing unset+"none"→
  "(default)" is therefore honest, not a conflation; dispatch delta (2) satisfied (unset never renders
  "none"). DEFAULT_EFFORT_LEVELS = `["minimal","low","medium","high"]` is the real legacy ladder
  (model-switch.js:30), pin-blessed, not invented. Empty-ladder-with-reasoning is genuinely reachable
  on the wire (independent fields end-to-end). **No finding.**
- **P3 (ModelSwitch):** busy-gate **matches a real wire constraint** — the serf server refuses a
  mid-turn model set with `Conflict` (appwire_runtime.go:658-664, `processing || reservedTurnID`); the
  legacy also gated the button on the same `isBusy(status, activeTurnId)` (model-switch.js:54-61).
  Escape/outside-click follow the sanctioned `widgets/menu` idiom (menu/index.tsx:192-203); Combobox
  renders its listbox inline (no portal), so option-clicks land inside `pickerRef` — no premature-close
  bug. Accessible names present (trigger "Change model", Combobox aria-label "Model"). **No finding.**
- **P4 (PendingChips):** derive-only confirmed — `usePendingTurnEntries` is a pure memoized selector,
  PendingChips adds no store state and filters `method !== "queue"` via `useMemo`; `PendingMethod`
  union and QueueStrip-owns-queue match the W5 model; queue-exclusion mutation is credible. **No finding.**
- **P5 (palette /tasks):** wired end-to-end — pre-existing command `run: clickTrigger("[data-tasks-trigger]")`
  (commands.ts:474) + T4's new `data-tasks-trigger` attribute on the Tasks `<button>`; `clickTrigger`
  does `querySelector().click()`, and the pre-existing test proves that click opens the Sheet. **No finding.**
- **P6 (deferrals) — all legitimate:** (a) location cluster — ThreadModel carries no location fields
  and `hydrateThread` (reducer.ts:232-266) drops `cwd`(771)/`gitInfo.branch`(157-162)/`projectPath`(762);
  rendering it needs `model.ts` + `reducer.ts` edits, both off-limits → deferral is *required* by the
  constraints, not just permitted. (b) showCost — only `Turn.cost` (types.gen.ts:1037) crosses the
  wire; no thread-level cost; summing loaded turns undercounts → honest deferral. (c) palette /status —
  StatusRow is rendered unconditionally (SessionChrome.tsx:44); no details panel to toggle, so a
  `[data-details-trigger]` would be a dead affordance; `clickTrigger` no-ops safely. All legitimate.
- **P7 (gates + manifest):** gates green (above). Diff scope authoritative: all 12 files within
  `panes/session/chrome/**` + `panes/session/pending/**` + the report; zero chokepoints, zero
  `reducer.ts`/`tokens.css`, zero sibling-stream files, zero Go, zero palette-command edit (the /tasks
  wiring used the pre-existing W6 command plus a chrome-side attribute only).

## Wire-truth citations backing the verdict (verified by the reviewer)

- **Reasoning independence (P2 / DEFAULT_EFFORT_LEVELS pin):** `SupportsReasoning()` returns
  `p.reasoning` (profile.go:323); `ReasoningEffortLevels()` returns `p.effortLevels` (profile.go:328).
  Live enrichment sets them from **independent** conditions: `clone.effortLevels` only when
  `len(info.ReasoningEffortLevels) > 0` (profile.go:442); `clone.reasoning = true` when
  `info.SupportsReasoning` (profile.go:454). They reach the thread snapshot independently
  (session.go:794-795 → appwire_projection.go:747-748). ⇒ `supportsReasoning:true` + empty ladder is
  reachable; the 4-level fallback is warranted.
- **none ≡ absence ≡ default (P2):** `NormalizeReasoningEffort("none") == ""` (reasoning_effort_test.go:75,
  alongside null/off/false/0); `ClampReasoningEffort` passes `"" || "none"` through unchanged "so the
  provider can decide" (llm/types.go:668-673); `providercfg/load.go:76-77` states serf's "none" clears
  the effort to the provider default **"rather than forcing an explicit disable."** No distinct state
  to honor ⇒ collapse is correct.
- **Mid-turn model-set refusal (P3):** `handleAppThreadModelSet` returns `appwire.Conflict("turn … is
  active")` when `processing || reservedTurnID != ""` (server/appwire_runtime.go:646-664). (`agent`'s
  `Session.SetModel` itself does not turn-guard — the refusal is at the appwire server boundary, before
  SetModel is called.)

## Findings

### Minor
- **M1 — report test-count arithmetic is wrong (+16, not +19).** The report claims "baseline 3217,
  +19 = 3236." Per-file deltas measured base→HEAD (`git show e3b9c188c:<file>` vs HEAD) are
  ModelSwitch +4, StatusRow +4, TasksPanel +1, statusFormat +2, PendingChips +5 = **+16**; the only
  changed test files are these five, so the whole delta is +16. HEAD (3236) and the all-green result
  are correct and independently verified, which makes the true baseline **3220**. The obligation
  ("count went up, no silent exclusions") is met; only the stated baseline/delta are off by 3.
  Correct the report to `baseline 3220, +16`.
- **M2 — RED-evidence value mis-attributed.** The report's Dispatch-#1 RED line pairs the epoch-0
  input `totalWorkMillis(45_000, new Date(0).toISOString(), 1_800_000_000_000)` with the return
  `1806000045000`. That value is actually the **pre-epoch** case's return
  (`new Date(-6_000_000_000)`, node-confirmed 1,806,000,045,000); the epoch-0 input returns
  **1,800,000,045,000**. Both are ≠ 45_000, so the RED direction holds and both tests bite under
  mutation (reviewer-confirmed 3 failures) — only the narrative input/output pairing is crossed.

## Notes (not findings — flagged for controller awareness)

- **N1 — PIN-E literal vs spirit.** The dismiss `useEffect` registers `document`-level `keydown`
  (Escape) and `mousedown` (outside-click) listeners. These are ephemeral (open-gated, cleaned up on
  close) overlay-dismiss handlers — the exact `widgets/menu` idiom (menu/index.tsx:200) — not a
  persistent global **chord** like ⌘K/⌘B. PIN-E's stated scope ("global keydown owners", "global
  chord", "third global listener") is chord ownership; reading it to forbid Escape-dismiss would also
  condemn `widgets/menu`/`widgets/combobox`. Judged **MET in spirit**; surfaced so the controller can
  ratify the reading rather than have a literal-only probe miss it.
- **N2 — busy-gate predicate is a conservative subset of the wire's.** UI gates on
  `active && activeTurnId` (AND); the wire refuses on `processing || reservedTurnID` (OR), i.e. stricter.
  Any residual gap (wire would refuse, UI still enabled) degrades gracefully to a `Conflict` toast via
  `handlePick`'s catch — matches the deliberate `isTurnActive` "both-landed" design. Not a defect.
- **N3 — /status palette command now permanently inert.** With `[data-details-trigger]` intentionally
  never added (P6c), the W6-defined "Toggle session details" command is a safe no-op forever. That
  command lives in `shell/palette/commands.ts` (outside T4's manifest); reconciling it (remove the
  command, or add a details surface) is a shell-owner / T8-sweep item, not a T4 defect.
- **N4 — honest caveat verified.** The report's disclosure that the `current !== "none"` normalization
  is not independently mutation-observable in jsdom is **accurate**: removing it leaves all 19 StatusRow
  tests green (reviewer-confirmed). Kept correctly for real-browser controlled-select correctness.

## Recommendation (P1 — root-cause-at-source vs defense-in-depth)

The statusFormat consumer-side rejection is sufficient for StatusRow today (it is the only consumer of
`activeTurnStartedAt`, and it is now robust). But the actual root cause is `epochMsToISO`'s asymmetry
in `reducer.ts` — it guards `undefined` but not a present `0`/negative, and it converts **eight**
timestamp fields (startedAt/completedAt/observed*/turn timestamps at reducer.ts:120,121,153,199,211,
212,349 plus activeTurnStartedAt:262). Any of those carrying a Go zero-time would surface the same
epoch sentinel. I recommend the controller's reducer wiring commit fix `epochMsToISO` at the source —
map a non-positive `ms` to `undefined` (Go zero-time is never a legitimate wall-clock instant for any
of these fields) — which protects every consumer uniformly, and KEEP the statusFormat guard as
defense-in-depth (the ThreadModel type still admits a string, and a future live-push producer could
reintroduce a bad anchor). Source fix primary, consumer guard retained — not either/or.

## Bottom line

The code is correct, honestly traced to the wire, gate-green, and every deferral is not merely
permitted but constraint-required. The two Minor findings are figures in the report `.md`, not defects
in the deliverable; correcting them (and ratifying N1) is a report-hygiene pass, not a fix round.
**APPROVED.**
