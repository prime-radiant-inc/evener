# Web Rewrite Wave 5 — Report (M5 interaction layer)

Status: COMPLETE (pre-merge). T1 chokepoint + addendum, four parallel streams (T2 composer, T3
queue/optimistic-pending, T4 ask_user dock, T5 session chrome), controller integration wiring, four
adversarially-reviewed close fix streams (F1 reducer honesty, F2 composer cluster, F3 lazy-chunk
flake, F4 tokenFlood probe flake), and this close task (T6: steer-copy rename, parity sweep, live
proof, gates, report). Wave branch: `w5-interaction`, HEAD before this task `5d25dd893`; this task
adds the steer-copy commit (`4e3518433`) plus this report/artifacts commit on top. NOT yet merged to
integration — that is the controller's serial step (W5 before W7), outside this task's scope.

## What shipped

- **T1 — interaction chokepoint** (`e67bb953c`..`e299f4803`, +addendum `1ee3ab60f`): the store/model
  surface every stream builds on — `ThreadModel` gains capabilities/goal/context/usage/reasoning
  fields (T1-A), the full Conflict-aware turn + session-action store surface (T1-B), the
  send/queue availability derivation helper (T1-C), the toast failure convention (T1-D), the
  Composer/SessionChrome slot carve with `Session.tsx` frozen (T1-E), `Textarea` autogrow becomes
  scrollHeight-based (T1-F), and the send/queue gate matched to legacy `renderer.js:479-513`
  verbatim. The addendum added `listModels`/`listTasks` store actions that unblocked the chrome
  stream's model-switch and tasks panel.
- **T2 — composer core** (merge `4eb55d47d`): `Composer.tsx` — send/steer/queue/drain routing off
  T1's derivation, per-ref drafts (localStorage, restore-on-mount), enter-to-send preference (a
  local hook this wave; the real prefs store is M6), attachments (paste/drag/picker → base64
  InputItem with in-textarea `[image N]` markers, PNG re-encode, 8-image/8-MiB limits), the generic
  `Dropzone` widget, interrupt affordance.
- **T3 — queue strip + optimistic pending** (merge `4093977db`): `QueueStrip.tsx` rendering from
  `model.queue`, edit = restore-text-to-composer-THEN-cancelQueued (loser-safe order),
  promote/cancel with `expectedEntryId` Conflict handling, drain-as-steer; optimistic pending as
  reducer/store-owned declarative state with a 10s timeout reaper (not a DOM-chip registry port),
  applied uniformly to send/steer/queue/drain.
- **T4 — ask_user answering dock** (merge `9d8289c6f`): `AskDock` rendering pending questions
  (shared parsing extracted into `askShared` — the transcript card's own rendering untouched),
  answer composition to the byte-exact `[answers]` format submitted via plain send (no dedicated
  wire method exists), per-ref batch/answer bookkeeping so late-arriving questions are never swept
  into an in-flight settlement, Conflict drops the composed text into the composer and never
  auto-retries.
- **T5 — session chrome** (merge `37aeb5a73`): the status row (state dot, model chip, reasoning
  effort, work-time clock, context gauge, usage), mid-session model switch (Combobox off
  reasoningEffortLevels/models), the session-actions menu (fork/aside/compact/clear/shutdown/rename)
  with destructive-action confirmation, goal display/set (snapshot + remount-safe optimistic
  override), the tasks panel (aggregate rows, honest empty state — unblocked by the T1 listTasks
  addendum), and steering classification that suppresses task-bookkeeping kinds (a sanctioned
  cross-wave edit to the wave-4 `SteeringItem.tsx`).
- **Controller integration wiring** (`262d3548a`..`095877f6d`, report `2510b8adc`): barrel-exported
  the `Dropzone` widget, wired `QueueStrip` into Composer's T3 slot and `AskDock` into its T4 slot,
  made pending-tracking uniform across all four submit methods, and retired the T3/T4 slot
  placeholder with a full-tree integration sweep (`Composer.integration.test.tsx`). Its review
  found three real seam bugs, all fixed in F2 (below).
- **Close fix streams F1–F4** (each on its own branch, adversarially reviewed, merged): see the
  close-fix section below.

## Key stories

**T2's critical stale-text revert.** `TextEditor.read()`/`write()` closed over a stale `text` state
value instead of a synchronous ref, so a submit that read the composer's text mid-render could
revert freshly-typed characters — a silent content-loss class. Fixed in `ddf05d8bf` (report addendum
`9859ef965`) by making `read()`/`write()` go through `textRef.current`, the same synchronous mirror
every liveness-sensitive read in the composer now uses (`getComposerText`, the drain snapshot). This
is why the whole composer reads its own live text through a ref, never a closure.

**F1's askPending "two concepts" discovery.** `askPending` is a wire-authoritative attention signal
(from hubcore's DeriveAttention), but the reducer was ALSO recomputing/clobbering it from item
lifecycle events — conflating "the daemon says an ask is pending" with "the transcript currently
shows an unanswered ask_user item." These are two different concepts that disagree during the
round-trip of answering. F1 (`a45644eef`) made the reducer stop clobbering the wire-authoritative
flag from item lifecycle; the transcript-derived "which questions are live" signal stays a separate,
purely positional computation (`deriveAskQuestions.ts`), layered with stateful batch membership
(`reconcileBatches.ts`) for the in-flight case.

**T5's cancelled-tone correction.** A review flagged (Important) that a cancelled task status was
rendering in the `danger` tone — but colour is reserved for attention, and a user-cancelled task is
a neutral outcome, not an error. Fixed in `69ac9f9b8`: cancelled reads neutral. This is the
"cancelled-tone neutral per colour-is-attention" decision recorded below.

**T4's batch state machine.** A purely positional "which ask is live" signal can't distinguish "my
own reply's echo" from "a sibling ask_user call that landed first" during the network round-trip of
sending an answer. `askDockStore` layers a per-ref batch state machine on top: a sending batch is
frozen at submit, only the submitted questions settle on the authoritative `USER_INPUT`, and late
questions survive under a fresh anchor and stay independently sendable.

## Close fixes (F1–F4)

- **F1 — reducer honesty** (`034ff8546`, `a45644eef`; report `d03f33b3a`; merged separately): carry
  a tool-result item's error into `ItemModel` on both the live and snapshot paths (adding
  `ItemModel.error`), and the askPending wire-authority fix above. Adversarially reviewed.
- **F2 — composer cluster** (`ab2b0838a`..`7a1b8e0b5`, `b07832dd7`; report `f0a915922`; merge
  `da76bd92b`): the three integration-wiring seam bugs plus related hardening — gate the strip-drain
  on `hasPending` (a mid-encode attachment must block the drain with the same toast every other
  submit path uses, Concern #3); gate the strip-drain's unconditional composer clear behind the
  same unchanged-since-submit check the classic drain uses, and share busy state so the two Steer
  controls disable each other (Concern #2 + the shared-busy gate); strip submitted attachment
  markers from the editor on send; announce composer restoration through the aria-live region on
  ask-pending exit; a store-level duplicate-text FIFO reconcile test; assert the pending queue chip
  is visible in the composed tree; and the floor-doc seam-divergence annotations (queue-edit
  text-only §1, ask-Conflict draft-preserve §2). Adversarially reviewed.
- **F3 — lazy-chunk flake root cause** (`4409f60d0`; report `w5-close-f3-report.md`; merge
  `9a6a2920e`): the flake cluster was NOT toast-singleton state bleed (the standing suspect,
  disproven). Root cause: a cold `React.lazy(() => import(chunk))` boundary racing Testing Library's
  default 1000 ms `findBy*` deadline under CPU load — the Suspense fallback (`null`) is still
  mounted when `findByText` times out. Two test files carried a degraded warm-up (they awaited
  `./index`, which only `import type`s the component — erased at runtime, leaving the real lazy chunk
  cold). Fixed both by awaiting the real chunk in `beforeAll` — awaiting the real completion, not
  widening a deadline.
- **F4 — tokenFlood probe flake** (`5fafb208f`; disclosure `6a1ebab22`; report
  `w5-close-f4-report.md`; merge `5d25dd893`): the 500-delta mounted-`<Session>` render-count probe
  is compute-bound, not async-stalled — 500 synchronous React commits inside the single test's
  5000 ms wall starve past the wall under CPU oversubscription. Every observed failure was the wall,
  never the `rerenders === 0` assertion (which held at 0 in every starved run). Fixed by shrinking
  the flood 500 → 100 (mechanism-level: less work under the tripwire, not a widened ceiling;
  mutation-proven the guard keeps its teeth). The disclosure commit also records a masked-exit-code
  gate incident caught during this stream.

## Steer-copy rename (this task, item 0)

Renamed the QueueStrip drain button `"Steer now"` → `"Steer queue now"` (Jesse-approved copy,
2026-07-21). The composer's own `"Steer"` button is unchanged. Commit `4e3518433`, RED-first: the
drain-button test matchers were switched to the new exact name first and watched fail (the button
still read "Steer now"); the rename made them pass; then the trap the close brief warned about
manifested and was fixed — the pre-existing composer-button disambiguator was a negative-lookahead
`/^steer(?!\s*now\b)/i`, which after the rename matched BOTH labels (the lookahead sees "queue" and
passes), so `getByRole` found two buttons. Replaced with exact / intent-revealing matchers: the
drain button matches the exact accessible name `"Steer queue now"`; the composer's own control
matches a readable predicate (`startsWith("Steer") && !includes("queue")`) rather than an exact
`"Steer"` — its real accessible name is `"Steer Shift+Enter"` (it carries the Shift+Enter `KeyHint`,
verified empirically), so the brief's literal `{ name: "Steer" }` would not have matched. Every
matcher across the Composer, QueueStrip, and integration suites was updated; a repo-wide sweep for
"steer now" under `src` is clean (zero stale references).

## Parity sweep

Both floors (`docs/web-ui/parity/contracts-composer-queue-pending.md` — Composer/Queue/Pending/
Attachments/Drafts/AskUser/Escalations; `docs/web-ui/parity/parity-m5-composer.md` — sections A–J +
its four cross-cutting M5-decision findings) plus the plan's chrome bullets were swept exhaustively
via four parallel read-only research packages, each row assigned VERIFIED-MET / CONSCIOUSLY-DIVERGED /
GAP with a citation; the controller independently verified the load-bearing gaps against source. The
full record — every gap and divergence with `path:line` citations, plus per-section verdict tallies —
is `.superpowers/sdd/w5-close-t6-parity-sweep.md`. Compact summary:

**Verdict shape.** The rewrite is a faithful, well-documented port. Composer and Queue are essentially
at parity (divergences are documented or beyond-parity); the pure logic — send/queue gating, queue
multiset/FIFO reconcile, `[answers]` byte-exact composition, escalation dedup/hydration — is strong
and cited. Divergence concentrates in three areas: optimistic-pending PRESENTATION + failure handling,
the ask_user TRANSCRIPT representation, and failure feedback delivered as toasts rather than inline
banners. The two aria-live strings match verbatim ("Answer the agent's questions." /
"Message composer ready."), and the ask-Conflict draft-preserve + base64 attachment contract were
confirmed as their floor-doc notes claim.

**Gap punch list (severity-ranked):**

- **HIGH — denied/errored `ask_user` renders an interactive answerable card.**
  `askDock/deriveAskQuestions.ts:39-41` gates only on `status==="completed"` with no `item.error`
  check; the projector stamps `Status:"completed"` even on error
  (`appwire_projection.go:437`), so a denied ask with parseable args shows a live card that would
  send an `[answers]` reply for an already-failed call. Legacy guarded it (`renderer.js:5676`). Fix:
  check `item.error` — belongs with the absorb roster's `ItemModel.error` consumption.
- **HIGH — sandbox-escalation resolve doesn't treat a Conflict as terminal.**
  `stores/threads.ts:850-854` deliberately omits `mapConflict`; a resolved-elsewhere escalation stays
  retryable-forever with a generic error instead of settling to "Escalation expired" (legacy keys off
  `serfErrorInfo==="conflict"`). No data loss; self-heals on reconnect. Flagged by two packages.
- **MEDIUM** — send/steer/drain optimistic entries have no visual chip (only queue renders; the
  question `w5-task-3-report` deferred to this sweep — a real gap for steer/drain); model-switch
  trigger not busy-gated; model picker not Escape/outside-click dismissable; `DEFAULT_EFFORT_LEVELS`
  fallback dropped (a reasoning model with an un-enumerated ladder shows no effort selector); ask_user
  transcript re-architecture (no `[data-ask-anchor]`, no `.ask-settled-line`, dock not `form`-owned —
  a documented wave-4 choice, unratified, M9/M10 must decide); location cluster (branch/worktree/cwd)
  absent from chrome; OS-notification "asks" loudScope not ported; "/" command palette not implemented
  (M6).
- **LOW** — no in-place "Allowed once"/"Denied" escalation state (card unmounts); no compact ask
  settled-line; pasted-image name uses raw `File.name` not `paste-<ts>.png`; empty-steer sets no
  placeholder hint; `📎` chip prefix dropped; provider tabs → flat Combobox; external model-change
  doesn't close an open picker; no visible state word; `document.title` not updated; `writeFor`
  fork-child staging absent; draft key string + `serf-hub.draft.new` fallback deltas.

**Consciously-diverged (recorded/structural, not gaps):** optimistic-pending failure handling →
toast-and-remove; failure feedback → toasts not banners; transport AppWire JSON-RPC not REST; plain
send now optimistic too (beyond-parity); async cap-refresh machinery dropped; iframe suppression moot
(dockview same-document); per-session stream reopen replaced by a persistent connection + durable
per-ref subscription; cross-session draft leak-guard moot under dockview remount-per-ref; ask-Conflict
draft-preserve (Jesse 2026-07-21); queue-edit text-only; work-time/cost promotion (finding #4); pane
badge never "Question waiting" (finding #1, no regression). All cited in the raw sweep.

## Live proof

Real hub + a real `serf serve` daemon + a real `oai-work/gpt-5.4-mini` session (switched live to
`oai-work/gpt-5-nano`), driven end to end through Chrome. No mocks. Evidence:
`.superpowers/sdd/w5-close-t6-evidence/` (10 screenshots).

**Environment note (coordination finding for Jesse).** serf-hub takes a **host-global flock at
`$HOME/.serf/hub.lock`** (`cmd/serf-hub/main.go:133-135`, "single hub per host"). At run time the
parallel **W7 close's own live-proof hub was already holding that flock**, so a normal hub on the
wave-4 precedent port could not start. Rather than disturb a sibling's in-flight run, I launched a
**fully isolated hub under a fake `HOME`** (its own `hub.lock`, `~/.serf/run` rendezvous, and state
root — all `HOME`/XDG-derived, verified at `rendezvous.go:40`, `config.go:89`) on port 19281, and
confirmed W7's hub stayed up and untouched on its own port throughout. This is the same isolation the
test suite uses (`t.Setenv(SERF_STATE_DIR, t.TempDir())`), not a flock bypass. **The two parallel
closes contend on the single-hub-per-host flock — worth noting for any future parallel live-proof
scheduling.**

| # | Journey | Verdict | Evidence |
|---|---|---|---|
| 2 | **ask answer round-trip** | **Pass (strong)** | The model made a real `ask_user` call ("Asked: [Color]"); `AskDock` rendered the "Which color do you prefer?" radiogroup (red/blue + "Something else…" + "let serf decide" + fallback "do that: I will choose blue." + skip + note); the composer's `_inputCard` carried `inert` (lockout **verified via DOM**), and the `aria-live` region read **"Answer the agent's questions."**; answering (blue → Send answers) settled the ask, the composer's `inert` cleared, and the `aria-live` announced **"Message composer ready."** (both strings verified in the live region). The transcript's own `[answers]` reply was byte-exact: `1. [Color] → "blue"`. `01/02-*.png` |
| 3 | **model switch mid-session** | **Pass** | "Change model" opened the flat searchable Combobox (aria-label "Model"), 82 models filtered live on "gpt-5"; selecting `oai-work/gpt-5-nano` closed the picker and the chip updated to `oai-work/gpt-5-nano` via the real RPC. `03/04-*.png` |
| 5 | **goal set** | **Pass (strong)** | "Set goal" → typed a goal → Save; chrome flipped "No goal set" → the goal text, then to **"Goal: active · 0 iterations"** and the goal engine drove the agent autonomously (it patched files + posted progress updates in the scratch project). "Clear goal" stopped it and restored "Set goal". |
| 6 | **tasks panel live** | **Pass** | The goal-engine agent created a 5-task list; the chrome counter showed **"Tasks 0/5"** and the Tasks slide-over rendered all five per-task rows live (Establish wave-4 parity baseline, Scaffold wave-5 interaction layer skeleton, Add parity-regression harness, Execute parity tests and iterate, Update planning/docs…). `06-*.png` |
| 7 | **attachment paste round-trip (base64 PNG contract)** | **Pass (strong)** | A synthetic `paste` of a 48×48 PNG inserted the `[image 1]` marker and a chip showing `paste-test.png` + `48×48`; sending base64-encoded it into the turn input; the transcript rendered the image thumbnail ("Image 1 of 1"); and the model's reply — **"The colors are magenta and cyan."** — exactly matched the generated PNG's colors, proving the base64 image reached and was processed by the model. `07/08-*.png` |
| 4 | **fork + aside** | **Pass (menu) / gated** | The Session actions menu rendered all six actions — **Fork, Aside, Compact, Clear, Shut down, Rename**. Fork and Aside were **disabled while a turn was active** (a busy-gate on those actions — note the contrast with the model-switch trigger, which the sweep flags as NOT busy-gated). `05-*.png` |
| 1 | **send / steer / queue / edit / promote under load** | **Partial — capability-limited daemon** | Send verified repeatedly (real turns, streaming responses, turn separators with real metrics — e.g. `5.8s · ↑17k ↓576`, `53s · exit 0` for a 40s shell tool). The composer's own **Steer control is present with accessible name exactly `"Steer Shift+Enter"`** — a live confirmation of item 0's matcher decision (the brief's literal `{ name: "Steer" }` would not have matched). But the manually-started `serf serve` daemon advertises **`interrupt`/`steer`/`queue` capability = false**: across both a tool turn and a text-streaming turn, Stop/Steer/submit were all correctly disabled during `active`, and the submit never entered "Queue" mode — so no queue could form. The queue strip ("Steer queue now" drain + edit/promote/cancel) and the shared-busy gate between the two steering surfaces therefore could **not** be exercised live. This is a **daemon-config limitation of a bare `serf serve` (hub-spawn is Wave 6, unavailable)**, not a UI defect — the composer gates its controls correctly per the reported (absent) capabilities. These behaviors are covered by the green unit + integration suites (`QueueStrip.test.tsx` 28 tests, `Composer.integration.test.tsx`, `pendingReconcile`) and by the parity sweep above. `09-*.png` |

Also confirmed live in passing: the full session chrome renders (state dot, model chip, reasoning-effort
dropdown, work-time clock, context gauge `used / window`, goal control, Tasks, Session actions —
`00-*.png`); shell tool calls render as collapsed rows; the attention model surfaces an awaiting
session in the sidebar's NEEDS YOU tier (a session that is both live and awaiting appears in both LIVE
and NEEDS YOU — confirmed against `/api/tree`, not a duplicate bug).

No Critical (content-loss / crash) surfaced. The one honest shortfall is journey 1's under-load
steering/queue, blocked by the test daemon's capabilities rather than by the UI.

## Decisions

- **Steer-copy applied per Jesse** — the drain button reads "Steer queue now" (approved 2026-07-21).
- **Ask-Conflict draft-preserve** (approved by Jesse, 2026-07-21) — on a `turn/start` Conflict from
  the ask-answers path, the rewrite merges the composed `[answers]` text AFTER any pre-ask composer
  draft (append-after-blank-line), rather than legacy's `dropComposedTextIntoComposer` unconditional
  overwrite. AskDock only hides/inerts the composer's input row and never clears its `text` state, so
  a pre-ask draft survives underneath the whole time. Annotated in both floor docs
  (`contracts-composer-queue-pending.md`, `parity-m5-composer.md` §C).
- **Queue-edit restore is text-only** — editing a queued entry restores its text to the composer;
  dropped image attachments surface via a `reportRemovedImages` warning toast, never auto-restored
  (the integration deliberately reuses the queue-edit merge behavior for the ask-fallback seam too).
- **Cancelled-tone neutral, per colour-is-attention** — a cancelled task status renders in a neutral
  tone, not `danger`; colour is reserved for attention (F5/T5 review, `69ac9f9b8`).
- **Hide-don't-clear ask lockout** — while an ask is pending, the composer's input surface is
  hidden + `inert` (not cleared), so its draft `text` state survives for the draft-preserve decision
  above; the aria-live region announces "Answer the agent's questions." / "Message composer ready."
  on each transition.

## Corrections (honest record)

- **The "wave-plan floor-count drift A=17/G=16" close-roster item was a controller transcription
  artifact — the text never existed.** The F2 close brief stated the wave-plan doc declares floor
  counts "A=17" and "G=16"; F2 (report §10a, probe 8) grepped the actual plan file for every
  spacing/case variant of that pattern and found no such statement anywhere. It was not a case of
  looking in the wrong file — the specific claimed drift text does not exist in the plan. Recorded so
  the close's finding trail stays honest.
- **The flake cluster's "toast-singleton state bleed" premise was disproven.** The standing suspect
  for the wave's intermittent test failures was toast-singleton cross-test state bleed. F3 disproved
  it and found two genuinely distinct root causes: (a) a cold `React.lazy` chunk boundary racing
  Testing Library's 1000 ms `findBy*` deadline under load (F3), and (b) a separate compute-bound
  render probe whose 500 synchronous commits starve past the 5000 ms whole-test wall under CPU
  oversubscription (F4). Both fixes attack the mechanism (warm the chunk / cut the work), neither
  widens a deadline — consistent with the standing "ceilings are tripwires only" rule.

## Go follow-ups (for Jesse)

1. **The projector hardcodes a completed tool item's `Status` to `"completed"` regardless of
   error.** `internal/appprojector/appwire_projection.go:437` stamps `Status: "completed"` on the
   `commandExecution` item unconditionally, even though line 435 carries `Error: data.Error`. So an
   errored/denied tool call reaches the frontend as `status:"completed"` with a populated `error`.
   This is the root cause behind the HIGH parity gap in the sweep (a denied `ask_user` still renders
   an answerable card, because the frontend's ask-derivation trusts `status === "completed"` and does
   not yet check `item.error`). A frontend fix (check `item.error`) is correct given the wire won't
   distinguish via status; a wire-side fix (a real terminal status for errored items) would resolve
   the whole class.
2. **`exitCode` typed field + escalation `resolved` broadcast — already SHIPPED on main via
   wire-honesty; the frontend absorb is pending.** The wave-4 Go follow-ups (shell exit-code as a
   typed wire field; a `serf/sandbox/escalation/resolved` broadcast for multi-client card clear) have
   landed on main through the wire-honesty work. The frontend has not yet absorbed them — see next
   steps.

## Next steps (NOT done in this task)

- **The absorb roster** (frontend catches up to the shipped wire-honesty work): regenerate
  `types.gen.ts`; have the shell tool consume the typed `exitCode` and `ItemModel.error`; reconcile
  the three stale rendering comments; add the escalation-`resolved` broadcast reducer case. (The HIGH
  ask-card gap below belongs with the `ItemModel.error` consumption work.)
- **The serial merges** — W5 to integration FIRST, then W7 second. This close does NOT merge
  anything; the controller owns the serial merge.

## Verification

Gates run from `cmd/serf-hub/frontend`, AND-chained (each gates the next), `vitest` bare (exit code
unmasked, output captured to a log read separately — never piped through tail/grep inside the chain):

```
npx tsc --noEmit  → EXIT=0
npx vitest run    → EXIT=0  (136 files / 2034 tests — identical to the pre-task baseline;
                             item 0 changed matchers + one label only, added/removed no tests)
npm run lint      → EXIT=0  (biome ci, 409 files, no fixes)
npm run build     → EXIT=0  (dist/PLACEHOLDER restored via `git restore`; `git status` clean after)
```

This branch has no Go changes, so the frontend gates suffice (the Go follow-ups above are pre-existing
wire facts, not changes made here). The RED-first trail for item 0 is recorded in its own section:
drain matchers switched to the new exact name → witnessed RED (button still "Steer now") → label
renamed → GREEN, with the negative-lookahead trap witnessed and fixed in between.

**Commit trail (this task):** item 0 `4e3518433` (`webui wave5 close: rename queue drain to "Steer
queue now"`), then this report + the parity-sweep + evidence artifacts.

**Live-proof housekeeping.** The isolated hub (PID, port 19281) and its `serf serve` daemon (port
19351) were both killed and confirmed gone (`ps`/`pgrep` show no serf/serf-hub binaries; ports 19281
and 19351 free); the goal engine was cleared before teardown; the browser tab was released to
`about:blank`; the built `serf`/`serf-hub` binaries were removed; the worktree is clean except this
report and its artifacts. The parallel W7 hub was never touched (verified up and unaffected on its own
port throughout, then observed to exit on its own). No credential material was echoed into logs or
screenshots.
