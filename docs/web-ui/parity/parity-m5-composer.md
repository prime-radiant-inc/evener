# M5 behavior-parity checklist — composer + turn interaction

- Source: `cmd/serf-hub` in this worktree, branch `worktree-webui-workspace-shell`, commit `8974a0a679d2cc8d6883650d17ee4d15186f79d4` (2026-07-20).
- Purpose: enumerate every observable behavior of the legacy hand-rolled JS composer so the workspace-shell rewrite (M5: composer send/steer/queue, queue strip, drafts, attachments, ask_user, escalations, status row, model switch) can be checked off item-by-item instead of eyeballed. Scope per `docs/superpowers/specs/2026-07-20-webui-workspace-shell-rewrite-design.md` §13.
- Every item cites `path:line` (or a line range) in the CURRENT implementation. Check a box only once you've confirmed the replacement does the same thing (or you've gotten explicit sign-off that the behavior is intentionally dropped/changed).

## Files read

Primary (as requested):
- `cmd/serf-hub/assets/renderer.js` (7090 lines total; read via targeted sections covering the composer/submit send-vs-steer-vs-queue decision, `ask_user` caching + whole-set resolution, sandbox-escalation cards, queue-strip promote/edit/cancel, status-row refresh timing, and composer keyboard bindings — NOT read start-to-finish; the transcript/tool-rendering portions of this same file are `parity-m4-transcript.md`'s scope, not duplicated here)
- `cmd/serf-hub/assets/pending.js` (254 lines, read in full)
- `cmd/serf-hub/assets/drafts.js` (149 lines, read in full)
- `cmd/serf-hub/assets/composer-attachments.js` (426 lines, read in full)
- `cmd/serf-hub/assets/model-switch.js` (386 lines, read in full)
- `cmd/serf-hub/templates/partials/input_strip.html` (15 lines, read in full)
- `cmd/serf-hub/web_format.go` (236 lines, read in full)

Pulled in because they're load-bearing for the requested behaviors (not optional reading — the requested files call straight into them, or a claim couldn't be verified without them):
- `cmd/serf-hub/assets/thread-state.js` (20 lines, read in full) — the single `isBusy()` predicate the composer, model-switch, and search palette all share.
- `cmd/serf-hub/assets/appwire.js` (targeted read, ~80 lines around `optimisticCall`/`startTurn`/`steer`/`queueTurn`/`drainAsSteer`) — the only way to verify which wire calls actually register an optimistic-pending chip vs. issue a bare `request()`.
- `cmd/serf-hub/assets/renderer-panels.js` (targeted read, ~70 lines) — Interrupt is wired through this file's generic session-action click delegate, not through `renderer.js`'s composer-binding code.
- `cmd/serf-hub/templates/partials/workspace.html` (full composer markup block, lines 1-115) — the actual DOM `input_strip.html` and `model-switch.js` bind against, and the source of the server-rendered disabled/capability attributes that duplicate the client-side gating.
- `cmd/serf-hub/web_workspace.go` (targeted read, `detailsSections`/`tokensAndCostRows`/`contextMeterHTML`, lines 200-400) — work-time, cost, and the full context meter are computed here, not in `web_format.go`, despite the milestone doc's one-line grouping of them under "status row."
- `hubapi/attention.go` (`StateWord`, lines 1-90) — needed to confirm exactly what the `askPending` boolean changes in the state label.
- `cmd/serf-hub/jstest/test-pending-registry.js`, `cmd/serf-hub/jstest/test-show-cost-gating.js` (grepped, not read in full) — corroborate live-wiring-vs-unit-test-only reachability for two findings below.

Not read (out of scope for composer + turn interaction; tracked by sibling milestone parity files or not part of M5): `renderer-tools.js` / `renderer-format.js` (M4, transcript item rendering), `spawn.js` / `search.js` / `notifications.js` / `theme.js` (M6), `settings-*.js` (M7), `sidebar.js` (tree/sidebar attention badges — cross-referenced only, not enumerated here), the rest of `appwire.js` beyond the optimistic-call/startTurn seam.

## Cross-cutting findings that need an explicit M5 decision

These are verified, code-level facts, each surprising enough that a straightforward rewrite would
plausibly diverge from current behavior without noticing. Each also appears as a checkbox below in
its proper section — flag a decision (keep vs. fix) for each rather than silently picking one.

1. **The workspace pane's own status badge can never say "Question waiting."** Every call site that
   builds a session pane's `StateLabel` hardcodes `stateLabel(state, askPending=false)`, so the
   `awaiting` state always renders "Your move" — even while a live `ask_user` card sits on screen.
   Only the sidebar/tree rows and OS-notification loudness gating use the askPending-aware label.
   `cmd/serf-hub/web_workspace.go:183,476,484,539,726`; `cmd/serf-hub/web_format.go:41-47,196-198`.
   See §C, last item.
2. **Plain sends don't use the optimistic-pending-chip mechanism that steer/queue/drain use.**
   `appwire.js`'s `startTurn` is a bare `request()`, never `optimisticCall()` — so a plain send gets
   no 10s-timeout/fail/retry chip; it gets a simpler, later, post-success-only local echo instead
   (`appendLocalUserMessage`). `cmd/serf-hub/assets/appwire.js:513-548`. See §A/§E.
3. **Conflict recovery is inconsistent across the four submit paths.** `sendAskAnswers` and the
   sandbox-escalation resolve both special-case `serfErrorInfo === "conflict"` with bespoke
   recovery UI; plain send, steer, queue, and drain do not — a `turn/start` Conflict on an ordinary
   send just shows a generic "send failed" banner. `cmd/serf-hub/assets/renderer.js:6285-6288` vs
   `:6585-6589`. See §A/§C/§D.
4. **"Work time" and "cost" aren't in the compact status row today.** Despite the milestone doc's
   one-liner ("status row … work-time clock … cost"), both currently render ONLY as rows in the
   separate details panel; the compact `#input-status` strip shows state/location/context/liveness
   only. Context itself is rendered twice, with different formatting, in the two places.
   `cmd/serf-hub/web_workspace.go:336-356` vs `cmd/serf-hub/templates/partials/input_strip.html:1-15`.
   See §I.

---

## A. Send / Steer / Queue / Interrupt — the state-gated decision

- [ ] Busy predicate is centralized: `state === "active" && !!activeTurnId`, nowhere else redefined — `cmd/serf-hub/assets/thread-state.js:16-18`. `activeFlags` is deliberately never consulted (serf daemons never populate it) — `cmd/serf-hub/assets/thread-state.js:8-9`.
- [ ] `turnAcceptsActions(state)` (send path stays open) is true for `active` OR `awaiting` — `cmd/serf-hub/assets/renderer.js:558-562`.
- [ ] `turnIsRunning(state)` (Stop/steer offered, Enter routes to Queue) is true ONLY for `active` — `awaiting` is "your move," not running, and shows plain Send — `cmd/serf-hub/assets/renderer.js:564-569`.
- [ ] Send-vs-Queue capability precedence on every `updateThreadState` call, in this exact order — `cmd/serf-hub/assets/renderer.js:479-513`:
  1. `ended`/`closed` → send=false, queue=false, button disabled, title "send unavailable" (line 484-488).
  2. Else if the source already advertised live `send`/`queue` capabilities for the CURRENT state (`liveCapabilitiesStatus === state`) → use those booleans directly, not state-derived defaults (line 489-496).
  3. Else if `state === "active"` and `liveQueueCap === false` is explicitly known → both disabled (line 497-501).
  4. Else if `state === "active"` → send=false, queue=true (queue-mode default) (line 502-506).
  5. Else (idle/awaiting/other) → send=true, queue=false (plain-send default) (line 507-511).
- [ ] At submit time the composer reads capability from the send button's own `data-capability-send`/`data-capability-queue` DOM attributes (the snapshot `updateThreadState` last wrote), not by recomputing from `this.state` — `cmd/serf-hub/assets/renderer.js:6497-6498`.
- [ ] That read is asymmetric: `canSend` defaults TRUE when the attribute is missing or not exactly `"false"` (fail-open); `canQueue` defaults FALSE unless the attribute is exactly `"true"` (fail-closed) — `cmd/serf-hub/assets/renderer.js:6497-6498`.
- [ ] Submit is a no-op (no request) when the composer is empty of both text and attachments — `cmd/serf-hub/assets/renderer.js:6491`.
- [ ] Submit is fully suppressed while an `ask_user` reply is pending (`this.pendingAsk` set) even if the (hidden+inert) composer form somehow submits — `cmd/serf-hub/assets/renderer.js:6479-6482`.
- [ ] Submit blocks with an error banner ("image attachment is still processing") if any staged attachment is still mid-encode (`item.pending === true`) — checked identically in both the submit and steer paths — `cmd/serf-hub/assets/renderer.js:6487-6490` (submit), `:6361-6364` (steer).
- [ ] When `canSend` is false and `canQueue` is true, submit calls `queueText` instead of `startTurn`; the send button is disabled for the duration of that one request only — `cmd/serf-hub/assets/renderer.js:6505-6524`.
- [ ] When neither capability is available, submit shows "send is not available for this session" and does nothing else — `cmd/serf-hub/assets/renderer.js:6525-6528`.
- [ ] A successful plain send does NOT go through the optimistic-pending registry (`pending.js`) — `appwire.js`'s `startTurn` is a bare `request()`, never `optimisticCall()` — `cmd/serf-hub/assets/appwire.js:513-518` vs `:520-548` (steer/queueTurn/drainAsSteer, which do call `optimisticCall`). Its local echo instead comes from `appendLocalUserMessage`, called only AFTER `startTurn` resolves successfully — `cmd/serf-hub/assets/renderer.js:6538-6542,6567-6574`. Net effect: plain sends have no 10s-timeout/fail/retry chip the way steer/queue/drain do (see §E).
- [ ] A plain-send failure (including a `turn/start` daemon Conflict) is reported with a single generic `"send failed: " + err.message` banner — there is no `serfErrorInfo === "conflict"` special case on this path, unlike `sendAskAnswers` (§C) or the sandbox-escalation resolve (§D) — `cmd/serf-hub/assets/renderer.js:6585-6589`.
- [ ] On successful send, the composer textarea and staged-attachment snapshot are cleared ONLY if the textarea's value is unchanged since submit (a user who kept typing mid-flight keeps both the new text and the sticky draft) — `cmd/serf-hub/assets/renderer.js:6327-6337` (`clearComposerDraftIfUnchanged`).
- [ ] Steer button behavior forks on composer/queue state (kata 0bq1), 4 cases — `cmd/serf-hub/assets/renderer.js:6344-6369`:
  - text + empty queue → classic single-text `turn/steer` (Path A) — `:6377-6399`.
  - anything + non-empty queue, OR any attachments present → `turn/drainAsSteer` carrying the textarea text/items so the daemon appends-then-drains atomically (Path B) — `:6400-6443`.
  - empty textarea + empty queue → no request; sets a placeholder hint and focuses the textarea — `:6365-6369`.
- [ ] Classic steer (Path A) requires `this.activeTurnId` to be set; if not, shows "steer failed: no active turn" instead of sending — `cmd/serf-hub/assets/renderer.js:6379-6382`.
- [ ] Drain (Path B) has its own error subtype: a REST `x-serf-error-info: queuedDrainPartial` (or thrown `err.serfErrorInfo === "queuedDrainPartial"`) means the text was queued before the drain step failed — the draft/attachments are still cleared (already queued) and the banner reads "drain failed after queueing," distinct from an ordinary "drain failed" — `cmd/serf-hub/assets/renderer.js:6417-6426,6435-6441`.
- [ ] Interrupt (Stop) is wired through the generic session-action click delegate, NOT the composer's own submit handler: `[data-action-trigger]` clicks route to `triggerSessionAction(action)` — `cmd/serf-hub/assets/renderer-panels.js:124-130,137-166`. It is silent on success and shows an `action + " failed: "` banner on rejection; no optimistic state change.
- [ ] Interrupt/steer button `disabled` state is set in two places that must stay consistent: the server-rendered initial attribute (`workspace.html:87,90` — disabled unless `State=="active"` AND `ActiveTurnID!=""` AND the capability is true) and the client-side `syncTurnActionControls()` re-sync on every busy-state change — `cmd/serf-hub/assets/renderer.js:536-556`.
- [ ] The Stop button is not merely disabled but absent from the DOM entirely once `State` is `ended` or `closed` (and only rendered if at least one of Interrupt/Steer/Send/Queue capability is true) — `cmd/serf-hub/templates/partials/workspace.html:86-88`.
- [ ] The model-switch trigger and the interrupt/steer/send controls key off the SAME busy predicate (`SerfThreadState.isBusy`), so they always agree on "can the user act right now" — `cmd/serf-hub/assets/model-switch.js:54-56` vs `cmd/serf-hub/assets/renderer.js:536-556`.

## B. Queue strip — preview, promote, edit, cancel

- [ ] Queue preview list is rebuilt from authoritative wire state only (`this.queueState` populated by `thread/queueChanged`); there is no local/optimistic mutation of the list itself — `cmd/serf-hub/assets/renderer.js:6639-6650` (comment + `renderQueuePreview`).
- [ ] The wrap `[data-queue-preview]` is hidden when depth is 0 UNLESS an optimistic `turn/queue` pending chip is still in flight (`pending.hasQueueEntries()`), so a just-submitted queue entry doesn't visually disappear before the daemon confirms — `cmd/serf-hub/assets/renderer.js:6651-6656`.
- [ ] Each preview row is truncated to its first line server/daemon-side, then additionally visually capped at 140 characters client-side with a trailing "…" — `cmd/serf-hub/assets/renderer.js:6670-6672`.
- [ ] Every queue row carries the daemon-minted `entryId` (from `state.ids[idx]`) so promote/edit/cancel calls identify the specific entry even if the queue shifted under the preview — `cmd/serf-hub/assets/renderer.js:6681`.
- [ ] Promote (`⇧`, issue #22) calls `turn/promoteQueuedAsSteer` (or REST `/promote-queued`) with `{index, entry_id}`; there is no local mirror — success is entirely rendered by the daemon's `thread/queueChanged` (row removed) + `serf/steering/injected` (transcript shows it) — `cmd/serf-hub/assets/renderer.js:6725-6754`.
- [ ] Edit (`✎`, issue #23) is "cancel-and-recompose," not in-place editing: full text (from `state.texts[idx]`, never the truncated preview) is restored to the composer FIRST, then the queued copy is removed — text-first ordering so a losing race still leaves the user's text safely in the composer — `cmd/serf-hub/assets/renderer.js:6839-6849,6862-6868`.
- [ ] Edit is disabled (with an explicit title tooltip) for image-only queued entries (empty trimmed text) — cancel remains the only option for those — `cmd/serf-hub/assets/renderer.js:6702-6707`.
- [ ] Edit degrades honestly on an old daemon that sends no `texts` array at all: shows an error directing the user to cancel-and-retype rather than guessing/truncating — `cmd/serf-hub/assets/renderer.js:6850-6856`.
- [ ] Restoring text to the composer never clobbers what's already typed there: existing text is kept, restored text is appended after a blank line, and a synthetic `input` event fires so the sticky draft (`drafts.js`) stays in sync — `cmd/serf-hub/assets/renderer.js:6823-6837`.
- [ ] If an edited entry had image attachments, they are NOT restored to the composer (text-only recompose); the user sees a distinct warning banner naming the count of dropped image attachments — `cmd/serf-hub/assets/renderer.js:6866-6868`.
- [ ] Cancel (`✕`, issue #23) removes an entry with no recompose step; on failure the row stays exactly as it was and the banner says so (no silent removal) — `cmd/serf-hub/assets/renderer.js:6808-6821`.
- [ ] A row's edit+cancel buttons are disabled for the duration of an in-flight cancel/edit call on that SAME row, closing a double-click race (double-append text or a confusing second Conflict) — `cmd/serf-hub/assets/renderer.js:6789-6806` (`setQueuedRowActionsDisabled`); re-enable still respects the image-only edit-disabled rule.
- [ ] Promote/edit/cancel/queue REST fallbacks speak snake_case (`entry_id`, `removed_text`, `removed_images`) while the AppWire path keeps camelCase — the JS layer normalizes REST responses into the camelCase shape so callers see one shape either way — `cmd/serf-hub/assets/renderer.js:6763-6787`.
- [ ] `queueText` allows empty/whitespace-only text when attachments are present (image-only queue entries are valid); rejects only when BOTH text and attachments are empty — `cmd/serf-hub/assets/renderer.js:6614-6617`.

## C. `ask_user` question cards

- [ ] Questions are cached at `TOOL_CALL_START`, keyed by `call_id`, into `this.pendingAskCalls` (a `Map`) — the live `item/completed` push that eventually drives `TOOL_CALL_END` carries no `arguments_json`, so the args MUST be captured at START or they're unrecoverable — `cmd/serf-hub/assets/renderer.js:1630-1641` (cache), `:242-247` (field doc).
- [ ] The interactive card is built on ACK (`TOOL_CALL_END`), never on `TOOL_CALL_START` — a call with no ack (e.g. an interrupted turn) is never pending and renders nothing, with no separate "ghost card" cleanup path needed — `cmd/serf-hub/assets/renderer.js:1673-1677,5671-5679` (`handleAskUserAck`).
- [ ] A denied/errored ask_user call (`hasError` true) posts no card at all — `cmd/serf-hub/assets/renderer.js:5676-5677`.
- [ ] Questions from multiple `ask_user` calls in the same turn accumulate into ONE pending set, globally numbered in posting order; each item's stable key is `callId + ":" + idx` — `cmd/serf-hub/assets/renderer.js:5813-5845`.
- [ ] The pending set (`this.pendingAsk`) resolves as a WHOLE on any `USER_INPUT` notification, regardless of which client (this one or another) sent it — `cmd/serf-hub/assets/renderer.js:1556-1562,6167-6179` (`resolvePendingAsk`). Resolution is idempotent — a no-op once already resolved, so both this client's own optimistic path and the server's later echo land safely.
- [ ] Exception: if `ask_user` acknowledges MORE questions while a partial reply's `turn/start` is still in flight, only the SUBMITTED items settle; late items survive under a fresh transcript anchor and stay pending — `cmd/serf-hub/assets/renderer.js:6181-6210` (`resolveSubmittedAsk`).
- [ ] Typed prose sent through the ordinary composer (not the ask dock) is ALSO a valid ask_user reply and resolves the pending set optimistically, the same as the dock's own send — `cmd/serf-hub/assets/renderer.js:6575-6579`.
- [ ] `askDockEl()` places the interactive form INSIDE the composer form itself (`form[data-input-form] [data-ask-response-dock]`), not as a separate transcript widget — the transcript keeps only a static, non-interactive anchor line — `cmd/serf-hub/assets/renderer.js:5681-5695,5817-5827`.
- [ ] While an ask is pending, the real composer surface AND textarea are set both `hidden` and `inert` (`setComposerAskMode(true)`) — not just visually hidden — and an `aria-live="polite"` status region announces "Answer the agent's questions." / "Message composer ready." on each transition — `cmd/serf-hub/assets/renderer.js:5697-5736`.
- [ ] Escape does NOT dismiss a pending ask — the dock is the one canonical response surface and there is no alternate "collapse" state to escape to (a documented no-op) — `cmd/serf-hub/assets/renderer.js:6155-6165` (`bindAskCardEscape`).
- [ ] Re-rendering the dock (e.g. a new question appended) preserves keyboard focus by key+selector+index snapshot/restore when the previously focused control still exists after rebuild; otherwise falls back to auto-focusing the first unanswered control on initial activation — `cmd/serf-hub/assets/renderer.js:5738-5786`.
- [ ] "N of M answered" footer count only counts items with a non-null `resolution`; Send is ALWAYS enabled regardless of that count — an unanswered question composes as `"skipped (no answer)"`, so there's nothing to gate — `cmd/serf-hub/assets/renderer.js:6142-6153,7013`.
- [ ] Each question offers exactly 5 resolution kinds, mutually exclusive, enforced by one central mutator (`setQuestionResolution`): `option` (single or multi per `multi_select`), `free` text, `decide` (with optional `leaning` text), `fallback` (only when `if_unanswered` is present), and `skip` — `cmd/serf-hub/assets/renderer.js:5874-5882,6093-6140`.
- [ ] Recommended options sort first via a stable sort (non-recommended options otherwise keep the model's given order) — `cmd/serf-hub/assets/renderer.js:5975-5981`.
- [ ] Clicking an already-active "special" control (free / decide / fallback / skip — NOT a regular option) a second time clears the resolution back to `null` (toggle-off); regular options don't get this toggle-off behavior — `cmd/serf-hub/assets/renderer.js:5933-5945,6044-6060`.
- [ ] The free-text and "leaning" inputs update the live resolution on every keystroke via `input` events (`skipRender: true`, so the control doesn't fight the user's own typing) — `cmd/serf-hub/assets/renderer.js:6007-6009,6024-6026`.
- [ ] A per-question free-text "note" field attaches to WHICHEVER resolution is chosen (option/free/decide/fallback/skip alike), not just to free-text answers — `cmd/serf-hub/assets/renderer.js:6064-6082`.
- [ ] `sendAskAnswers` submits through the SAME `startTurn`/`turn/start` path the ordinary composer submit uses — never a parallel send API — `cmd/serf-hub/assets/renderer.js:6247-6250,6271-6276`.
- [ ] A `turn/start` Conflict on the ask-answers path (another client's reply won the daemon's atomic reservation) is deliberately NEVER auto-retried: the composed `[answers]` text drops back into the ordinary composer for the user to decide, and the pending ask is discarded (not settled) — `cmd/serf-hub/assets/renderer.js:6258-6260,6283-6288,6232-6245`.
- [ ] The composed reply text format is byte-exact / golden-string tested: `"[answers]\n1. [header] → <resolution>\n..."`, one line per question in global posting order, each line optionally suffixed `" — note: <note>"` — `cmd/serf-hub/assets/renderer.js:7016-7029`. Per-kind resolution text: options join by `, `; free is `free text: "…"`; decide is `you decide` (+ `— leaning: "…"` if present); fallback is `do your stated fallback (<if_unanswered>)`; unresolved is literally `skipped (no answer)` — `cmd/serf-hub/assets/renderer.js:7000-7013`.
- [ ] Once settled, the interactive card is replaced by a single dim `.ask-settled-line` reusing the `ask_user` tool renderer's own `target()` summary formatter for "what was asked," plus the reply clipped to 160 chars — never left as a live/interactive element — `cmd/serf-hub/assets/renderer.js:6212-6230`.
- [ ] `discardPendingAsk` (used by replay, session swap, and the Conflict-recovery path above) is explicitly a non-settlement teardown: it leaves no settled transcript line, unlike `resolvePendingAsk` — `cmd/serf-hub/assets/renderer.js:5801-5811`.
- [ ] Re-hydration (reconnect / session swap) clears `pendingAskCalls` and calls `discardPendingAsk()` before replay resumes, so a stale in-flight `ask_user` call-cache from the previous view can never leak into the new one — `cmd/serf-hub/assets/renderer.js:1248,1258`.
- [ ] The composer's OWN status badge (input-telemetry strip) does not reflect ask-pending at all: every workspace-pane call site of `stateLabel(state, askPending)` passes `askPending=false` unconditionally, so the `awaiting` state always renders "Your move," never "Question waiting," even while an ask_user card is live on screen. That distinction (`hubapi.StateWord`, `hubapi/attention.go:58-80`) is used only by the sidebar/tree entries (`AskPending` wired via `cmd/serf-hub/web_api_tree.go:656`, consumed by `cmd/serf-hub/assets/sidebar.js:61-71`) and OS-notification loudness gating (`cmd/serf-hub/assets/notifications.js:268`) — **verify intentionally**: `cmd/serf-hub/web_format.go:41-47,196-198`; `cmd/serf-hub/web_workspace.go:183,476,484,539,726` (every call site, all hardcode `false`).

## D. Sandbox escalation cards (M7 human-gated approval)

- [ ] Triggered by the `SANDBOX_ESCALATION_REQUESTED` notification, routed straight to `appendSandboxEscalation` — `cmd/serf-hub/assets/renderer.js:1543-1545`.
- [ ] Also surfaced from the `thread/read` snapshot's `thread.serf.pendingEscalations` array on cold attach / reconnect (the "surface-on-entry" path), through the SAME render function — `cmd/serf-hub/assets/renderer.js:943-945,4281-4291`.
- [ ] De-duplication by `escalationId` (`this.shownEscalationIds`, a `Set`) prevents the live notification and the snapshot surface-on-entry from double-rendering the same card, in either arrival order — `cmd/serf-hub/assets/renderer.js:4301-4308`.
- [ ] The de-dupe set is reset on every re-hydration (DOM clear on reconnect), because clearing the DOM also destroys any rendered card — a still-pending escalation must be able to re-render after reconnect, while a since-settled one correctly does not reappear (absent from the fresh snapshot) — `cmd/serf-hub/assets/renderer.js:1259-1264,316-319`.
- [ ] The card is deliberately styled/labelled as a HARNESS prompt ("Sandbox approval — requested by serf, not the agent"), never as model output, specifically so the model can neither emit nor influence the Allow control via social engineering — `cmd/serf-hub/assets/renderer.js:4293-4296,4318-4321`.
- [ ] Body text names the blocked tool, the denied path, and the active `--sandbox` mode verbatim — `cmd/serf-hub/assets/renderer.js:4325-4328`.
- [ ] A `kind === "shell"` variant (not produced in v1 — the shape is reserved for a future seccomp-notify design) additionally shows the partially-executed command + output-so-far and a caveat that Allow re-runs the command start-to-finish, not resume-in-place — `cmd/serf-hub/assets/renderer.js:4331-4351`.
- [ ] The card settles on the resolve request's OUTCOME, never optimistically — Allow/Deny disable both buttons immediately (no double-submit) but the "Allowed once"/"Denied" state only replaces the action row after the daemon actually accepts it — `cmd/serf-hub/assets/renderer.js:4366-4368,4375-4396`.
- [ ] Rejection is split by CAUSE, not merely by whether an error code was present: only `serfErrorInfo === "conflict"` (already resolved elsewhere) is TERMINAL and settles the card to "Escalation expired (already resolved)"; every other failure (transport error, transient daemon-unavailable) leaves the card pending, re-enables both buttons, and shows a retry-inviting note — `cmd/serf-hub/assets/renderer.js:4369-4396`.
- [ ] Deny sends `approve:false` explicitly — there is no "dismiss without answering" affordance, because that would leave the daemon's blocked tool-exec goroutine hung forever — `cmd/serf-hub/assets/renderer.js:4407-4409`.

## E. Optimistic pending registry (`pending.js`) — integration semantics

- [ ] Registered methods and their live callers, verified against `appwire.js`: `turn/steer`, `turn/queue`, `turn/drainAsSteer` DO register a pending chip before the request is sent (`optimisticCall`) — `cmd/serf-hub/assets/appwire.js:520-548,204-223`. `turn/start` (plain send) is a bare `request()` and NEVER registers — `cmd/serf-hub/assets/appwire.js:513-518`. `pending.js`'s `turn/start` chip styling/preview-text branches (`cmd/serf-hub/assets/pending.js:70-73,131`) and the reconcile calls for it (`cmd/serf-hub/assets/renderer.js:712,718`) exist in the module's vocabulary and are unit-tested in isolation (`cmd/serf-hub/jstest/test-pending-registry.js`), but are unreachable dead paths in the current live wiring.
- [ ] A registered chip auto-fails after a fixed 10s timeout with no server confirmation (`timeoutMs` default) — `cmd/serf-hub/assets/pending.js:49,136-138`.
- [ ] `turn/queue` chips render as `<li>` inside the queue-preview `<ul data-queue-list>` (so they sit naturally among real queue rows); every other method's chip is a `<div>` placed directly in the transcript/conversation pane — `cmd/serf-hub/assets/pending.js:56-83,120-123`.
- [ ] A failed chip (`.optimistic-failed`) grows an inline reason line plus a "Retry" link; clicking Retry removes the failed chip THEN calls the caller's `onRetry(intent)` with the original `{method, text, items}` snapshot, so the user sees exactly one fresh pending chip in its place, never a stacked failed+new pair — `cmd/serf-hub/assets/pending.js:144-171`; the renderer wires `onRetry` back to the matching `SerfAppwire` call per method — `cmd/serf-hub/assets/renderer.js:661-674`.
- [ ] `tryReconcile` prefers matching a NOT-yet-failed entry over a failed one when both exist (two-pass: `preferFailed` false then true) — `cmd/serf-hub/assets/pending.js:196-204`.
- [ ] `turn/drainAsSteer` reconciliation ignores text entirely and matches first-come-first-served, because the daemon collapses the whole queue into one joined STEERING entry whose exact text the placeholder can't have predicted — `cmd/serf-hub/assets/pending.js:186-190`; the notification handler ALSO tries to reconcile any in-flight `turn/steer` placeholder against the same `serf/steering/injected` event, since drain and classic-steer both surface that way — `cmd/serf-hub/assets/renderer.js:685-694`.
- [ ] `turn/queue` chips reconcile against the AUTHORITATIVE queue-preview list from `thread/queueChanged` (a multiset match on normalized text, consumed one-for-one so one preview entry can't confirm two duplicate-text chips), not by simple text equality against a single event field — `cmd/serf-hub/assets/pending.js:207-248`; ids are snapshotted before mutating so `removeEntry` during iteration is safe — `:224-225`.
- [ ] The queue-preview wrapper's visibility (`hidden`) is driven jointly by authoritative depth AND `hasQueueEntries()` (still-pending optimistic chips) — see §B — `cmd/serf-hub/assets/pending.js:98-106`.

## F. Per-session drafts (`drafts.js`, issue #21)

- [ ] Storage key is `"serf-hub.draft." + sessionId`, or `"serf-hub.draft.new"` when the composer has no `data-session-id` (home/new-session surface) — `cmd/serf-hub/assets/drafts.js:19-27`.
- [ ] Every `localStorage` access is wrapped in try/catch; any failure (private mode, disabled storage, quota) degrades silently to "no draft," never breaks the composer — `cmd/serf-hub/assets/drafts.js:33-76`.
- [ ] Writing blank/whitespace-only content REMOVES the key rather than storing empty string — a draft that would never send is never persisted — `cmd/serf-hub/assets/drafts.js:57-61`.
- [ ] `bind(form)` restores a stored draft into the textarea ONLY if the textarea is currently empty; it never overwrites content already present (e.g. a re-bound composer mid-typing) — `cmd/serf-hub/assets/drafts.js:138-143`. Returns whether a draft was restored so the caller can re-run textarea autosize — `cmd/serf-hub/assets/renderer.js:6305,6313`.
- [ ] Cross-session leak guard: if the SAME form element survives a session swap with leftover text still in the textarea, `bind` clears it before restoring, UNLESS that leftover text is verbatim a DIFFERENT session's own stored draft (`isOtherSessionsDraft`), in which case it's also cleared — text belonging to session A is never silently written under session B's key — `cmd/serf-hub/assets/drafts.js:108-136`.
- [ ] `writeFor(sessionId, value)` stages a draft for a session with no live form required — used by the fork-from-message flow (issue #42) to pre-populate the FORK CHILD's composer before navigation; the child's own later `bind()` restores it like any typed draft — `cmd/serf-hub/assets/drafts.js:78-96`.
- [ ] `clear(form)` is called on every successful send/queue/steer/drain (not on failure) via `clearComposerDraftIfUnchanged` — `cmd/serf-hub/assets/drafts.js:68-76`; `cmd/serf-hub/assets/renderer.js:6336`.

## G. Image attachments (`composer-attachments.js`)

- [ ] Hard limits: max 8 attachments, max 8 MiB (`8 * 1024 * 1024`) per file — `cmd/serf-hub/assets/composer-attachments.js:21-22`. The 8-count cap is CUMULATIVE across the whole composer session (paste + drag + file-picker share one running total via `pendingState.items.length`), not reset per gesture — `:137-159`.
- [ ] Every accepted image — including already-PNG input — is re-encoded to PNG via an offscreen `<canvas>` round-trip, which strips color profiles/EXIF; this matches the TUI's "always re-encode pasted clipboard image data" rule — `cmd/serf-hub/assets/composer-attachments.js:24-50`.
- [ ] Rejection reasons are file-specific: non-image MIME → bare filename; over the 8-image cap → `"<name> (maximum 8 images)"`; over 8 MB → `"<name> (maximum 8 MB)"` — `cmd/serf-hub/assets/composer-attachments.js:120-135`.
- [ ] Each accepted attachment reserves a monotonically-increasing marker (`nextMarker`, never reused even after removal — removing chip 3 from [1,2,3] leaves the next attach at 4, so existing `[image N]` references already typed by the user stay stable) and inserts the literal placeholder text `"[image N]"` at the current cursor position — `cmd/serf-hub/assets/composer-attachments.js:52-63,109-118`.
- [ ] Removing a chip strips the FIRST literal occurrence of its `"[image N]"` marker from the textarea (plain string search, not regex) and shifts the cursor back if it sat past the deletion point — `cmd/serf-hub/assets/composer-attachments.js:91-107,261-271`.
- [ ] Paste: only image portions of the clipboard are intercepted; if the paste is text-only, the handler returns early and lets the browser's default paste proceed untouched — `cmd/serf-hub/assets/composer-attachments.js:192-194,161-174`. When BOTH an image and text are pasted together, `preventDefault` is deliberately NOT called, so the accompanying prose still lands in the textarea alongside the image chip — `:196-198`.
- [ ] Drag-drop wires `dragenter`/`dragover`/`dragleave`/`drop` on `[data-drop-zone]`; a `.drop-active` class toggles for visual feedback; `dragover` must `preventDefault` for `drop` to fire at all — `cmd/serf-hub/assets/composer-attachments.js:370-397`.
- [ ] File-picker: the visible attach button (`[data-attach-trigger]`) proxies a click to a hidden `<input type=file>`; the input's value is reset to `""` after every change so re-picking the identical file re-fires the change event — `cmd/serf-hub/assets/composer-attachments.js:403-417`.
- [ ] Drop and file-picker share one ingestion path (`ingestFiles`) and surface at most ONE rejection banner per gesture — the banner's text CONTENT is replaced, never appended, across repeated rejections — `cmd/serf-hub/assets/composer-attachments.js:339-363`.
- [ ] Rejection banners use a "gesture version" counter to discard stale async rejections: if a NEWER attachment gesture has started by the time an older gesture's re-encode fails, that stale rejection is silently dropped rather than overwriting the newer gesture's (possibly-empty) banner state — `cmd/serf-hub/assets/composer-attachments.js:70-74,339-342`.
- [ ] A successful paste auto-clears any stale rejection banner left over from a PRIOR drop/paste/file-picker gesture — attaching successfully counts as "the user moved on" — `cmd/serf-hub/assets/composer-attachments.js:219-223`.
- [ ] When no `[data-attachment-error]` banner element is reachable from the anchor, rejections still surface via `window.SerfToast` as a fallback — never silently dropped — `cmd/serf-hub/assets/composer-attachments.js:344-349`.
- [ ] Chip rendering: `📎 <name> (<width>×<height>)` with dimensions omitted only if either dimension is not a number (still-pending items) — `cmd/serf-hub/assets/composer-attachments.js:250-260`.
- [ ] Encoding stays as `ArrayBuffer` at this layer (not base64) specifically to avoid a 33% memory blow-up during composition; base64 conversion is deferred to the submit/fetch boundary — `cmd/serf-hub/assets/composer-attachments.js:8-12`.

## H. Model switch + reasoning effort (`model-switch.js`)

- [ ] The model-trigger button(s) are disabled whenever `SerfThreadState.isBusy` is true, tracked via `thread/status/changed` + `turn/started` + `turn/completed` notifications rather than by depending on `renderer.js`'s own subscription ordering — `cmd/serf-hub/assets/model-switch.js:1-11,54-69,304-333`.
- [ ] Opening the picker is a hard no-op while busy (defense in depth beyond the disabled attribute) — `cmd/serf-hub/assets/model-switch.js:216-218`.
- [ ] Model list is fetched once and cached (`modelsCache`); a failed fetch clears the cache so the NEXT open retries rather than repeating the same rejected promise — `cmd/serf-hub/assets/model-switch.js:71-83`.
- [ ] Picker groups models by provider (alphabetically sorted provider tabs); the tab containing the CURRENT model auto-activates on open — `cmd/serf-hub/assets/model-switch.js:136-160`.
- [ ] Picker dismisses on outside click or Escape; the outside-click listener is deliberately attached on a `setTimeout(…, 0)` so the SAME click that opened the picker doesn't immediately close it — `cmd/serf-hub/assets/model-switch.js:94-111`.
- [ ] Selecting a model closes the picker immediately (optimistically) and calls `setModel`; a rejection surfaces as a toast, with no rollback of the (already-closed) picker — `cmd/serf-hub/assets/model-switch.js:187-192`.
- [ ] Reasoning-effort vocabulary fallback: `supportsReasoning === false` is a KNOWN answer of "no levels" (empty array); an absent/empty ladder on a model that DOES reason (or whose support is still unknown/`undefined`) falls back to a hardcoded `["minimal","low","medium","high"]` ladder ported verbatim from `spawn.js`'s `DEFAULT_EFFORT_LEVELS` — `cmd/serf-hub/assets/model-switch.js:20-43`. `undefined` and `false` are deliberately distinct states.
- [ ] Effort/model chip state is cold-attach-seeded from a ONE-TIME `thread/read` snapshot read (`thread.serf` fields) at init — NOT from `/api/models` or the appwire model-list call, which carry only `{provider, model}` with no effort ladder — `cmd/serf-hub/assets/model-switch.js:250-280`.
- [ ] Live updates arrive via `thread/model/changed` (updates model text/title + effort ladder, closes any open picker) and `thread/reasoning-effort/changed` (updates just the current effort value) — `cmd/serf-hub/assets/model-switch.js:282-293,304-333`.
- [ ] The effort chip element is hidden entirely (`hidden=true`, empty text) whenever there is no current effort value, rather than showing a placeholder — `cmd/serf-hub/assets/model-switch.js:238-248`.
- [ ] After an `htmx:afterSwap` session-pane swap, `resyncAfterSwap` re-reads busy state from the fresh DOM, closes any stale picker, DROPS the cached model list, and re-fetches the effort snapshot — stale busy/picker/cache state must never bleed from the previous session into the newly-swapped one — `cmd/serf-hub/assets/model-switch.js:339-345`.
- [ ] Live notifications are matched to the currently-displayed session by ref/threadId before being applied; a notification for a different session/pane is dropped — `cmd/serf-hub/assets/model-switch.js:295-302`.

## I. Status row / composer chrome fields (state, location, context, liveness, work time, cost)

- [ ] **Composer chrome is split across two independently-swapped regions**, not one: the model+effort chips live directly in `workspace.html`'s composer bar (`cmd/serf-hub/templates/partials/workspace.html:70-83`), while state/location/context/liveness live in the separately htmx-swapped `#input-status` partial, `input_status` — `cmd/serf-hub/templates/partials/input_strip.html:1-15`, mounted at `cmd/serf-hub/templates/partials/workspace.html:99-111`.
- [ ] `#input-status` fetches `/_partials/s/{id}/state` on 3 triggers: page `load`, a custom `serf-hub:status-refresh` event bubbled from `document.body`, and a `30s` fallback poll — `cmd/serf-hub/templates/partials/workspace.html:100-104`.
- [ ] While (and only while) the thread state is `active`, a client-side 10s interval additionally fires that custom `serf-hub:status-refresh` event so the strip doesn't sit stale for up to 30s during an active turn; the timer starts on entering `active` and stops on leaving it — `cmd/serf-hub/assets/renderer.js:114-115,451-458,2960-2978`.
- [ ] The strip's live liveness handle (`[data-liveness]`) MUST be re-queried after every swap (`ensureLivenessEl`) since the innerHTML swap invalidates any cached reference — a stale/detached handle is treated as "not mounted" and no-ops rather than throwing — `cmd/serf-hub/assets/renderer.js:2947-2955`.
- [ ] State badge shows a dot + `StateLabel` computed server-side by `stateLabel(state, false)` (see §C's askPending finding) — `cmd/serf-hub/templates/partials/input_strip.html:4`; `cmd/serf-hub/web_format.go:41-47,196-198`.
- [ ] The location cluster (branch / worktree / cwd, each independently optional) is entirely omitted when `ThreadDocumentMode` is set — `cmd/serf-hub/templates/partials/input_strip.html:5-11`.
- [ ] Context in the compact strip shows ONLY `used / window` token counts (`CompactContextNumbers`, no percentage, no remaining) with a `⚠` warning glyph + `context-warn` class appended once `ContextPercent >= 80` — `cmd/serf-hub/templates/partials/input_strip.html:12`; percent is computed server-side as `int(status.ContextPressure * 100)` — `cmd/serf-hub/web_workspace.go:499`; the compact string itself is `"<used>k / <window>k"` with no "left"/percent text — `cmd/serf-hub/web_format.go:221-226`.
- [ ] **Work time and cost are NOT part of the compact input-telemetry strip at all** — despite the milestone doc's one-line grouping ("status row … work-time clock … cost"), both currently render only as `<dt>/<dd>` rows in the separate DETAILS PANEL (`[data-details-trigger]`-toggled slide-over), gated behind live-status-first/persisted-meta-fallback logic — `cmd/serf-hub/web_workspace.go:336-356` (`detailsSections`, "Usage" section). Formatting: `formatWorkMillis` reuses the same compact-duration convention as everywhere else (`"Ns"` under 1 min, `"Nm"` under 1 hour, `"Nh Nm"` above) — `cmd/serf-hub/web_format.go:79-102`. Cost is `appwire.EstimateCost(model, usage)` and is OMITTED (no row at all, not even blank) when it returns `""` (unestimable model) — `cmd/serf-hub/web_workspace.go:254-256`. **Decide explicitly whether M5 replicates this split (details-panel-only) or promotes work-time/cost into the compact status row.**
- [ ] Context is ALSO rendered a second, richer way inside the details panel — a visual meter bar plus "`N% used · used/window · remaining left`" stat line (`contextMeterHTML`), independent of the compact strip's plain numbers — `cmd/serf-hub/web_workspace.go:260-276,339-340`.
- [ ] `cost` row visibility is additionally gated by a body-level `data-show-cost` attribute (a settings toggle) hiding `[data-row="cost"]` via CSS — verified only in `cmd/serf-hub/jstest/test-show-cost-gating.js:3,67-68`; a rewrite needs the equivalent user-facing setting, not just the always-on details computation.
- [ ] `formatTokenCount` renders raw counts under 1000 verbatim, `Nk` (rounded to nearest thousand, ties away from zero via `+500`) at 1000+ — used identically for the compact strip AND the details-panel tokens row — `cmd/serf-hub/web_format.go:228-236`.
- [ ] Liveness is a THIRD, independent element inside the same strip (`[data-liveness]`, hidden by default) driven by a client-side 3s tick, not the htmx swap cycle — two honest thresholds: quiet at ≥20000ms silence (neutral, quantized "quiet ~Nm" text, dot keeps breathing) escalating to concern at ≥180000ms (amber + glyph + "may be stalled" + the breathing pulse stops) — `cmd/serf-hub/assets/renderer.js:101-110`.
- [ ] The workspace title updates via a dedicated `updateThreadTitle` path independent of the `#input-status` swap, and separately dispatches a `serf-hub:thread-title` DOM event for any listener (e.g. tab title / sidebar) — `cmd/serf-hub/assets/renderer.js:520-528`.

## J. Keyboard shortcuts & Enter-to-send preference

- [ ] `⌘/Ctrl+Enter` always submits (form `requestSubmit()`), regardless of the Enter-to-send preference — `cmd/serf-hub/assets/renderer.js:6885-6891`.
- [ ] Plain `Enter` submits ONLY when the Enter-to-send preference is on (read fresh from `localStorage["serf-hub.composer"].enterToSend`, default OFF/absent) AND no modifier key is held — `cmd/serf-hub/assets/renderer.js:6881-6884,6892-6897`.
- [ ] `Shift+Enter` is the keyboard equivalent of clicking the steer button — UNLESS Enter-to-send is ON, in which case Shift+Enter reverts to a literal newline (because plain Enter now sends, so Shift+Enter's old "escape hatch" meaning would double up) — the steer BUTTON itself stays clickable either way; only this keybinding changes — `cmd/serf-hub/assets/renderer.js:6898-6909`.
- [ ] `/` as the very first character of an EMPTY textarea opens the command palette instead of being typed; `/` anywhere else (non-empty textarea, or not the first char) is literal input — `cmd/serf-hub/assets/renderer.js:6910-6916`.
- [ ] All of the above submit/steer/palette keybindings are suppressed entirely when the renderer is running inside a framed side-pane iframe (`isInPane()`) — `cmd/serf-hub/assets/renderer.js:6878`.
- [ ] The textarea auto-grows on input up to 50% of the viewport height, then scrolls internally past that — `cmd/serf-hub/assets/renderer.js:6307-6314`.

---

*Behaviors NOT covered here (out of this checklist's stated scope, tracked elsewhere in §5 of the
rewrite design doc): tool-call renderers, transcript scroll/liveness-per-turn, subagent module,
sidebar/tree, settings, spawn, search/palette, PWA/notifications infrastructure beyond the
askPending wiring noted above, session actions (fork/aside/compact/clear/shutdown) other than
Interrupt, tasks/details panel structure beyond the work-time/cost/context rows, goal display.*
