# W5 close (T6) — parity sweep, full record

Exhaustive behavior-parity sweep of both floor docs plus the plan's chrome bullets, for the wave-5
interaction layer (`cmd/serf-hub/frontend/src/`). This is the auditable record M9/M10 trust; the
wave5-report carries only its compact summary.

**Floors swept:**
- `docs/web-ui/parity/parity-m5-composer.md` — sections A–J (send/steer/queue/interrupt, queue strip,
  ask_user cards, sandbox escalation, optimistic pending, drafts, attachments, model switch, status
  row/chrome, keyboard) + its four cross-cutting M5-decision findings.
- `docs/web-ui/parity/contracts-composer-queue-pending.md` — Composer, Queue, Pending/Optimistic,
  Attachments, Drafts, Ask User, Escalations (mined from `cmd/serf-hub/jstest/`).
- Plan chrome bullets (`docs/superpowers/plans/2026-07-21-webui-rewrite-wave5-interaction.md` T5 +
  design-doc §Composer/§Session-chrome).

**Method:** four parallel read-only research packages (one per floor slice), each assigning every row
one of three verdicts — VERIFIED-MET (cite implementing code/test), CONSCIOUSLY-DIVERGED (cite the
recorded decision), GAP (severity + what's missing). The controller (this task) independently
verified the load-bearing gaps against source (citations marked ✔ below).

**Verdict shape:** the rewrite is a faithful, well-documented port. Divergence concentrates in three
places: (1) optimistic-pending PRESENTATION and failure handling, (2) the ask_user TRANSCRIPT
representation, (3) failure feedback as toasts rather than inline banners. The pure logic — send/queue
gating, queue multiset/FIFO reconcile, `[answers]` composition, escalation dedup/hydration — is
strong and cited. Two correctness gaps stand out (both cross-cutting on the projector's
error-on-completed shape and on Conflict handling).

---

## Consolidated GAP punch list (severity-ranked — the trust-bearing output)

### HIGH

1. **Denied/errored `ask_user` still renders an interactive, answerable card.** `isAckedAskUserItem`
   (`panes/session/composer/askDock/deriveAskQuestions.ts:39-41`) gates only on
   `type==="commandExecution" && toolName==="ask_user" && status==="completed"` — **no `item.error`
   check** (grep-confirmed none anywhere in `askDock/*`/`askShared`). The projector hardcodes
   `Status:"completed"` even on error (`internal/appprojector/appwire_projection.go:437` ✔, while
   `:435` carries `Error: data.Error`), and F1 already carries that error into `ItemModel.error`. So a
   denied ask with parseable argumentsJSON shows a live card that, if answered, sends an `[answers]`
   reply for an already-failed call. Legacy guards this (`renderer.js:5676`: `if (hasError ||
   !questions.length) return`). **Fix:** exclude `item.error` in `isAckedAskUserItem` — belongs with
   the absorb roster's `ItemModel.error` consumption work. (Controller-verified ✔.)

2. **Sandbox-escalation resolve does not treat a Conflict as terminal.** `resolveEscalation`
   (`stores/threads.ts:850-854` ✔ — explicit comment "No mapConflict here") and
   `SandboxEscalationRail.handleResolve` (`transcript/tools/sandboxEscalation.tsx:141-160`) treat
   every resolve rejection identically: a generic "Couldn't resolve" note + re-enabled Allow/Deny. A
   Conflict (already resolved by another client) should render a distinct terminal "Escalation expired
   (already resolved)" state and never re-enable — legacy keys that off `serfErrorInfo==="conflict"`.
   Net: a resolved-elsewhere escalation is retryable-forever showing a confusing error until a
   reconnect clears it (no data loss; self-heals on reconnect). Flagged independently by two research
   packages. (Controller-verified ✔.)

### MEDIUM

3. **Send/steer/drain optimistic-pending entries have NO visual representation.**
   `usePendingTurnEntries` is consumed only by `queue/QueueStrip.tsx:124` for method `"queue"`.
   `Composer.tsx:363-391` registers pending entries for all four methods (for the 10s
   timeout/failure/reconcile), but nothing renders send/steer/drain — they are tracked and reaped
   entirely in-store, invisibly. Legacy rendered `.optimistic-pending` chips in the conversation pane
   for steer/drain and image-only `turn/start`. Net: no immediate optimistic feedback for
   steer/drain; the wire echo (or a failure/timeout toast up to 10s later) is the only signal (plain
   send was already chip-less in legacy). **This is exactly the question `w5-task-3-report.md:185-188`
   explicitly deferred to this sweep — finding: a real, unresolved gap for steer/drain.**

4. **Model-switch trigger is not gated by the busy predicate.** `chrome/ModelSwitch.tsx:103-112`
   disables only on `!capabilities.changeModel` (a static session cap, `web_session.go:486`), never
   the live `isTurnActive`/busy predicate that Composer's Stop/Steer use (`Composer.tsx:574,582`).
   `openPicker` (`:71-84`) has no busy check either. Breaks the legacy "model-switch and composer
   controls agree on can-act now" invariant; a mid-turn switch depends on daemon tolerance. Flagged
   by two research packages.

5. **Model picker is not dismissable by Escape or outside-click** — only its Cancel button
   (`ModelSwitch.tsx:86-88`; the Combobox's Escape/blur closes just its own listbox popup,
   `widgets/combobox/index.tsx:147-167`). Legacy dismissed on outside-click OR Escape.

6. **DEFAULT_EFFORT_LEVELS fallback dropped.** `chrome/StatusRow.tsx:57-103` no longer falls back to
   `["minimal","low","medium","high"]` for a reasoning-capable model whose ladder the hub doesn't
   enumerate. The Go hub can emit `supports_reasoning:true` with no ladder (`web_spawn.go:487-493`);
   the reducer coerces that to `[]` (`reducer.ts:262-263,584-585`), so the rewrite shows NO effort
   selector where legacy showed the 4-level default. Severity contingent on whether serf's live
   `thread.serf` ever emits that shape.

7. **Ask_user transcript re-architecture (no `[data-ask-anchor]`, no `.ask-settled-line`).** ask_user
   renders as a persistent read-only tool-call card (`transcript/tools/askUser.tsx`) plus the plain
   reply — there is no compact pending anchor and no dim settled line. The interactive dock is a
   composer-area sibling (not owned by `form[data-input-form]`, which doesn't exist), with per-batch
   footers rather than one pinned form-owned footer (`AskDock.tsx:95-102,150`). Documented in
   `askUser.tsx:1-21` as a wave-4 choice, but NOT among the recorded wave-5 decisions — **M9/M10 must
   ratify.** No content loss (arguably shows more); the DOM contract those contract rows assert
   (183/184/199-201/214/223) is absent. Sub-concern: with a long question list the per-batch Send can
   scroll with the questions — the exact thing legacy pinned against.

8. **Location cluster (branch/worktree/cwd) absent from the session chrome.** Only `branch`, only in
   the sidebar (`shell/rail/RailRow.tsx:205`); no cwd/worktree and no `ThreadDocumentMode` omission
   handling in the status row.

9. **OS-notification "asks" loudScope not ported.** No browser-`Notification`/loudScope module exists
   in the React frontend (grep-confirmed). The server-side awaiting→needs_you normalization is intact
   and the client consumes `askPending` + `serf/attention/changed`, but the client loud-scope behavior
   is absent this wave (likely M6/notifications territory).

10. **"/" command palette not implemented.** `Composer.tsx:461-480` has no `/`-first-char branch and
    no palette surface exists in `frontend/src`. Explicitly M6-scoped; `/` types literally (no crash).

### LOW

- **No settled "Allowed once"/"Denied" in-place state on escalation** — the card unmounts on success
  (`threads.ts:858-861`) rather than settling with a confirmation label. Anti-optimistic core (buttons
  disable immediately, only settle after the daemon accepts) IS met.
- **No compact settled line after an ask resolves** — the dock unmounts (`AskDock.tsx:135`); "never
  left interactive" holds; info persists as the read-only tool card + `[answers]` userMessage.
- **Pasted-image name** uses the raw clipboard `File.name` (`useAttachments.ts:142`), not a
  synthesized `"paste-<ts>.png"`; chip label and wire `InputItem.name` differ from spec.
- **Empty-queue+empty-text Steer sets no placeholder hint** (`Composer.tsx:432` focuses only).
- **`📎` chip prefix dropped** (dims logic intact).
- **Provider-grouped tabs → flat searchable Combobox** (`ModelSwitch.tsx:45-58`); capability intact,
  reshape needs M9/M10 sign-off.
- **External `thread/model/changed` doesn't close an already-open local picker.**
- **No visible state word** in the status row — state is conveyed by dot color + `aria-label` + the
  header Cadence.
- **`document.title` (browser tab) not updated; no `serf-hub:thread-title` event** — pane/tab-strip
  title is store-reactive instead.
- **`writeFor` fork-child draft-staging absent** — fork uses `forkFromTurn(editedInput)` RPC; the
  fork-from-message UI is out of M5 scope.
- **Draft key string** is `serf.composer.draft.v1.<ref>` vs the cited `serf-hub.draft.<sessionId>`;
  behavior fully met, no back-compat with old keys.
- **`serf-hub.draft.new` session-less fallback absent** — `Composer` requires a `ref`; no session-less
  composer this wave (Welcome has none; spawn pane is Wave 6).

---

## Consciously-diverged clusters (recorded decisions / structural, not gaps)

- **Optimistic-pending failure handling replaced by toast-and-remove** — no `.optimistic-failed`, no
  inline Retry link, no late-reconcile-of-failed, no live-over-failed preference, no attachment
  snapshot for retry; the 10s timeout survives as a toast. Recorded `w5-task-3-report.md §2` as a
  visible-to-Jesse change. ~8 pending rows.
- **Failure feedback is toasts, not inline banners** (queue/cancel/promote/attachment-rejection) — a
  consistent documented wave convention; the message text carries the same intent. Two sub-deltas:
  toasts stack rather than replace one banner, and don't auto-clear on the next clean success.
- **Transport is AppWire JSON-RPC, not REST** — `turn/queue`, `turn/drainAsSteer`,
  `turn/promoteQueuedAsSteer`, `turn/cancelQueued` with `expectedEntryId` (camelCase); no REST
  fallback path; `{index, entry_id}` payload semantics preserved.
- **Plain send now registers optimistic pending too** (INVERTED / beyond-parity) — `submitAction`
  wraps all four methods uniformly; legacy's plain `turn/start` was a bare request. Documented.
- **Async capability-refresh machinery dropped** — availability is derived synchronously from snapshot
  caps + status (`protocol/sendQueueAvailability.ts:12-45`); the stale-refresh-discard races are
  vacuous (no capabilities-changed wire push exists).
- **iframe keybinding suppression not ported** — dockview panels are same-document (grep-confirmed no
  iframes); the `isInPane()` gate is moot.
- **Per-session stream open/close/reopen replaced by one persistent connection + durable per-ref
  subscription** — the "reopen a stream on send" rows are vacuous by construction.
- **Cross-session draft leak-guard dropped** — VERIFIED moot: `panes/session/index.tsx:20-22`
  (non-singleton pane) + `shell/DockHost.tsx:37-49` (unmount/remount per ref) → no in-place DOM morph
  across refs; the guard reduces to correct per-ref keying.
- **Ask-Conflict draft-preserve** (approved Jesse 2026-07-21) — merges `[answers]` after any pre-ask
  draft (`Composer.tsx:280-317`, `restoreTextToComposer`) instead of legacy's overwrite. VERIFIED.
- **Queue-edit restore text-only** (dropped images → `reportRemovedImages` warning toast). VERIFIED.
- **Work-time/cost status split resolved by promotion** (parity finding #4) — work-time promoted into
  the compact `StatusRow`; dollar cost dropped (Go-side `EstimateCost`, never crosses the wire); token
  usage `↑in ↓out` shown instead; context unified into one meter+numbers.
- **Pane status never says "Question waiting"** (parity finding #1) — the `StatusDot` maps
  `awaiting`→`needs-you` (color + aria-label), no visible state word, no askPending awareness; the
  misleading legacy "Your move" text is simply gone; the ask signal is the AskDock card + its aria-live
  region. No regression.
- **base64 attachment contract** — items carry base64 `data` in memory (not ArrayBuffer) through
  composition; base64-at-submit is the wire contract (`encodePng.ts`, `threads.ts:304-311`). VERIFIED.

---

## Controller-verified aria-live + golden confirmations

- The two aria-live announcement strings match verbatim: **"Answer the agent's questions."**
  (`AskDock.tsx:152`) on ask entry, **"Message composer ready."** (`Composer.tsx:189`) on exit.
- `[answers]` composition is byte-exact vs legacy `renderer.js:6980-7031` including em-dashes and the
  `quoteGoString` C0/quote/backslash escaping (`askCompose.ts:39-92`).
- Escalation interactive resolve is real (approve/deny → `serf/sandbox/escalation/resolve`,
  `threads.ts:850-854`), non-optimistic, de-duped by `escalationId` via list-upsert
  (`reducer.ts:358-365`), and now mounted into the tree (`Session.tsx:145`) — the only interactivity
  defect is the missing conflict-terminal branch (HIGH #2).

## Per-section full row tables

The four research packages' full per-row tables (every VERIFIED-MET row with its citation) are
preserved verbatim in the task transcript; every row above is drawn from them. Verdict distribution:
Composer/Queue essentially at parity (divergences documented/beyond-parity); Pending-Optimistic logic
faithful, presentation diverged; Attachments/Drafts faithful; Ask-User dock strong (the transcript
representation re-architected per MEDIUM #7); Escalations wired and functional except HIGH #2.
