# Wave 8 — T3 (transcript parity) report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-transcript` (worktree `webui-w8-transcript`), base `e3b9c188c`
**Commit range:** `e3b9c188c..b50fbe211` (4 commits, one per cluster)
**Manifest:** `cmd/serf-hub/frontend/src/panes/session/transcript/**` (exclusive)
**Tests:** full suite `228 files / 3293 tests` all green (baseline `223 / 3217` → **+5 files, +76 tests**); tsc clean; Biome ci exit 0; `npm run build` clean + `dist/PLACEHOLDER` restored.

The one concern is a single-line controller wiring in the chokepoint `Session.tsx` to
activate the turn-failure recovery button (details in §Concerns). Everything else is
complete, wire-true, and mutation-net covered.

## Commits

| SHA | Cluster |
|---|---|
| `2b014212d` | `ItemModel.error` rendering in tool calls (force-open + failure marker) |
| `f6153de22` | `task_list` update cards (touched rows + progress head) |
| `3ac3dfdad` | steering classification (dividers by kind + in-transcript notification cards) |
| `b50fbe211` | turn-failure end-cap (taxonomy badge + retry/reconnect) |

## First-step trace (reducer off-limits — escalate, don't edit): NO escalation needed

Confirmed the reducer preserves everything the four clusters consume; **`reducer.ts` was
not touched**, and MW-C (a steering wire field) is **not needed**:

- **(a) `task_list` `argumentsJSON` on a settled item** — preserved. The live settle drops it
  (`appwire_projection.go:424-434`) but `mergeArguments` (`reducer.ts:167-171`) carries the
  existing item's `argumentsJSON` forward, and the reload path keeps it
  (`apptranscript.go:284,312`). The tool's authoritative `State` snapshot (`store.View()`,
  `session_tools_task.go:79/128/201/234`) rides `ThreadItem.raw`, which the reducer **does**
  drop — but the plan scoped the card to `argumentsJSON` + the output progress footer, so no
  reducer/Go change is required (see §Task cards / conscious divergences).
- **(b) `serf/steering/injected` classifiable content** — the classification is
  **content-pattern-based, pure frontend** (a faithful port of `renderer-format.js:414-494`,
  driven the way `renderer.js:4706-4756` drove it). `item.text` is all that's needed and it is
  mapped verbatim (`reducer.ts:695`, and the reload steering item carries `Text`,
  `apptranscript.go:253-264`). No wire-kind field is dropped ⇒ **MW-C unnecessary**.
- **(c) `turn.error`** — mapped by `wireToTurnScalars` (`reducer.ts:216`) as the wire `TurnError`
  (`appwire/types.go:507`, `types.gen.ts:1061`).

## Dispatch-time fold-in sweep (honest `Status:"failed"` on errored items)

Main now stamps `apptranscript.SettledToolStatus(data.Error != "")` → `"failed"` on errored
tool items, **live AND reload** (`appwire_projection.go:438`, `apptranscript.go:381`; the
`TurnStatusFailed` turn stamp at `:558`). My error rendering keys off **`item.error` presence
as primary** (present on old-daemon reloads whose status is still `"completed"`) with
**`status === "failed"` as corroboration** — `ToolCallItem.toolFailed()`.

Swept the manifest for completed-assumption comments the new status value breaks, and fixed
three (all stale since wave 5 mapped `ItemModel.error`):
- `tools/shellTool.tsx` header — "status is ALWAYS completed … hard-codes Status: completed"
  → corrected (a nonzero **exit** is a clean tool result with empty `error`/status
  `"completed"`; `exitCode` is the shell's orthogonal signal, handled in the descriptor; the
  tool-**error** path is the generic `ToolCallItem` treatment).
- `tools/bodies.tsx` (×2) — "ItemModel … never a structured … error" / "ItemModel drops the
  wire's error field entirely" → corrected.

No runtime status assumption breaks: `isItemLive` is `status === "inProgress"` (a failed item
is correctly not-live); `flow/useTranscriptScroll.ts:97` already keyed the error-anchor pill
off `turn.status === "failed" || turn.error !== undefined`, consistent with the new end-cap.

## Cluster 1 — `ItemModel.error` rendering (triage #2; floor §11:261, §2:100 / renderer-tools.js:589-594)

Rendered **generically once in `ToolCallItem`** so every current and future descriptor gets it
free (like `outputImages`):
- A failed row (`item.error` non-empty, or `status === "failed"`) **force-opens** at settle
  (folded into the existing edge-triggered `autoExpand` effect; a user's manual collapse still
  wins), surfaces the **error text verbatim** above the descriptor body, and earns a
  `<Chip tone="danger">Failed</Chip>` marker + `data-failed`/`data-attention="error"` attrs.
- A clean row stays glyph-less and collapsed ("success recedes, only failure earns the eye").
- A body-less/summary-only descriptor still becomes an expandable `<details>` when it errored,
  so the error is always readable.
- An empty-string error is not a failure (matches the projector stamping `failed` only when
  `data.Error != ""`).

This also means a **denied `ask_user`** transcript card now shows its denial (Failed chip +
error text) while staying read-only. (The separate composer-side "denied ask still answerable"
defect from w5-close HIGH #1 lives in `composer/askDock/deriveAskQuestions.ts` — **outside this
manifest**; flagged in §Concerns.)

## Cluster 2 — task / plan cards (Deferred #4/#7; floor parity-m4 §9:239, contracts §11:236)

New `tools/taskCard.tsx` registers a `task_list` descriptor. The tool-descriptor contract gained
one field, `suppress?(item): boolean`, honored in `ToolCallItem` (renders `null`) so a row can
vanish entirely:
- **`action:"view"`** and a malformed non-mutation with no error → **suppressed** (no card, no
  divider, no tool-call row) — fixes legacy regression Deferred #7a.
- A valid `append`/`update` → a `.task-card` (auto-expanded like the legacy always-visible card):
  progress head **`<done> / <total>`** parsed from the tool's own output footer
  (`"Progress: N/M tasks complete."`) + a neutral `Meter`, and one **touched row per changed
  task** — `append` rows show the description (flag `added`); `update` rows flagged
  `done`/`cancelled`/`started` show `#<id>` + notes (gated on `done|cancelled|in_progress`
  exactly like `renderer.js:5010`).
- A **failed** mutation renders **no card** — its error is surfaced by the cluster-1 generic
  path (mirrors the legacy card being appended only `if (!data.error)`), an improvement over the
  legacy silent drop.

**Conscious divergences (for T8's sweep):** the authoritative `State` snapshot is dropped by the
reducer, so — vs. the legacy card — an `update` row has **no description** (only `#id` + status +
note), there is **no full-list "show all" fold / surrounding-context rows**, and **no inferred
"and now working on X" auto-advance row**. The card shows only what the caller's own args prove.
A note-only update (no status change, no progress footer) shows the head with no per-row change.

## Cluster 3 — steering classification (Deferred #5/#7b; floor parity-m4 §8:207-217, contracts §17:314)

- New pure `messages/steeringClassify.ts` (`classifySteering` + `steeringTreatment`), a faithful
  content-based port. `SteeringItem.tsx` rewritten from its 2-way split to three treatments:
  - **suppress** — `current-task` / `full-list` / `task-nudge` (task bookkeeping the tasks panel
    owns; `task-nudge → nothing` fixes legacy regression Deferred #7b).
  - **divider** — `loop` / `read-only` / `transcript` / `tasks-done` / `unknown`, each now
    labeled by its **own classified kind** (sentence-cased at the display layer) instead of one
    generic "Steering injected".
  - **card** — `notification`, one `NotificationCard` per `<job-notification>`/observer-callback
    block; leftover non-block text keeps a plain divider.
- New `messages/NotificationCard.tsx` (contracts §17): blocks parsed **individually** (non-greedy,
  no cross-block aggregation), tone classification (`success|neutral` recede with no colour,
  `warning`→attention chip, `error`→danger chip — color-is-attention), demoted secondary line
  (job type / exit / reason), entity-decoded excerpt rendered as **escaped** text (XSS-safe) with
  a long-excerpt collapse, an always-present **raw disclosure**, a communicate **message rendered
  through the sanitizing `Markdown` widget** (8k-clamped), and concerns as a quiet note.

**Conscious scope-out (for T8's sweep):** the legacy card's full communicate **facts `<dl>`**
(`status`/`commit_hashes`/`test_summary`/`artifacts`) is **not** rebuilt — the markdown message +
concerns carry the signal; the plumbing facts stay in the raw disclosure. The watch/observer
glyph vocabulary (`◌`/`↩`) is replaced by the uniform tone treatment.

## Cluster 4 — turn-failure diagnostics (Deferred #6; floor parity-m4 §9:237, contracts §9/§10)

New `TurnFailureEndCap.tsx` + pure `turnFailure.ts` (`classifyTurnError` + `asTurnError`).
`TurnBlock` renders the end-cap when `asTurnError(turn.error)` is present (primary; corroborated
by the honest `status:"failed"`), between the turn's items and the separator:
- **Taxonomy badge** (danger `Chip`): `provider <status>` for a `cause.kind === "provider"` HTTP
  failure, `connection` for a hub-source or reconnect-class message
  (`isReconnectRetryDiagnostic`'s substrings, `renderer.js:4463-4471`), else the `source`, else
  `error`. Plus the error message and optional hint.
- **Recovery action** wired to the existing `threadsStore.send` (turn/start): re-issues the failed
  turn's originating user input (its `userMessage` item, `appwire_projection.go:131-168`). Labeled
  **"Retry"** for provider/other and **"Reconnect & retry"** for the connection class (a single
  call serves both — the hub auto-resume relaunches a dead daemon); a failed re-issue surfaces on
  the `useToasts()` singleton.

**Design-system note:** the failure "red" is carried entirely by the danger `Chip` (its tone is
allowlisted in `token-contract.test.ts`); the end-cap's own CSS module stays on neutral ink/edge
tokens because a **non-widget stylesheet may not reference `--danger`** (the allowlist is
widgets-only, confirmed against `styles/token-contract.test.ts` §(b)). Same posture as
`tools/sandboxEscalation`. The recovery button is `variant="primary"`, not danger — re-issuing a
turn is not destructive (color-is-attention).

## Concerns

1. **[chokepoint wiring — controller action] Turn-failure recovery needs one line in
   `Session.tsx`.** The end-cap's Retry/Reconnect button needs the session `ref`, which reaches
   `TurnBlock` only from `Session.tsx` — a **controller-owned chokepoint T3 must not edit**, and
   which T1 did **not** wire (`sessionRef` was not in T1's seam list). The feature is built and
   fully tested behind an **optional `sessionRef` prop** on `TurnBlock`; until the wiring lands,
   the diagnostic (badge + message + hint) renders in full and only the button is withheld
   (`canRetry` gate). **Controller one-liner** at `Session.tsx:182`:
   `renderRow={(index) => <TurnBlock turn={turnAt(index)} sessionRef={ref} />}`
   (There is no session-ref React context to read instead, and turn IDs are per-thread
   sequential so the ref cannot be recovered from `turn.id`.)

2. **[cross-manifest, informational] w5-close HIGH #1's answerable-denied-ask fix is not in this
   manifest.** Excluding `item.error` in `isAckedAskUserItem`
   (`composer/askDock/deriveAskQuestions.ts`) is a composer/askDock change, not `transcript/**`.
   T3's transcript-side error rendering (cluster 1) does surface a denied ask's error read-only;
   the composer gate is a separate stream's/controller's item.

## Design-system / discipline adherence

- Widgets only (`Chip`/`Button`/`Card`/`Meter`/`Markdown`); tokens-only CSS — every new class via
  `requireClass(...)`, no literal colors, no `styles.<name>` inside comments. Full suite (incl.
  `token-contract.test.ts` + `requireclass-contract.test.ts`) green.
- color-is-attention: task rows and steering dividers stay neutral; colour spent only on
  `error`/`failed`/`warning` via allowlisted widget tones. Sentence case; accessible button names.
- `reducer.ts` and `tokens.css` untouched. No new global keydown. No push, no merge.
- TDD RED-first with wire-true fixtures (real `formatTaskList`/`job_notify.go`/`TurnError`
  serialization); gates per commit AND-chained on real exit codes (tsc → bare vitest with
  file-count-up assertion → Biome → build + restore placeholder).
