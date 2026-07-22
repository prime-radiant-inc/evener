# Behavior contracts: composer, queue, pending/optimistic, attachments, drafts, ask_user, escalations

Mined from `cmd/serf-hub/jstest/`. Each line is a behavior a new Vitest suite must
re-cover, tagged with the jstest file it currently comes from. CSS/template-token-only
files (no runtime/DOM-interaction behavior, just regex assertions over `style.css` /
`templates/`) are listed by name only, per instructions. Files matched on `ask` that
were actually about `task` (`test-task-updated-subscription.js`, `test-tasks-panel.js`)
were excluded as filename false positives, not part of ask_user.

## Composer

### test-composer-shortcuts.js
- in the main workspace, Cmd+Enter submits the composer and Shift+Enter triggers steer (test-composer-shortcuts.js)
- with the enterToSend preference off (default), Cmd+Enter still submits and Shift+Enter still steers, identical to the unset default (test-composer-shortcuts.js)
- with enterToSend on, bare Enter submits and Shift+Enter no longer steers — it inserts a newline (default keydown behavior is left unprevented) (test-composer-shortcuts.js)
- inside a side-pane iframe, Cmd+Enter, Ctrl+Enter, and Shift+Enter are all no-ops — no submit and no steer (test-composer-shortcuts.js)
- the kbd hint next to the send/steer buttons switches between "⌘↵"/"⇧↵" and "↵"/hidden to match the current enterToSend setting (test-composer-shortcuts.js)

### test-composer-image-markers.js
- pasting an image inserts literal "[image N]" text at the textarea cursor and moves the cursor past it, with N starting at 1 (test-composer-image-markers.js)
- sequential attachments via paste, drop, and the file picker all draw from the same monotonically-increasing marker counter (test-composer-image-markers.js)
- removing an attachment chip strips only that image's "[image N]" substring from the textarea, leaving sibling markers untouched (test-composer-image-markers.js)
- marker numbering never reuses a removed number — the next attachment always gets highest-ever-assigned + 1, even immediately after removing the highest marker (test-composer-image-markers.js)
- resetting the marker counter (e.g. after send) restarts a fresh composer's numbering at 1 (test-composer-image-markers.js)
- removing a chip when no textarea is wired to the pending state still splices the item out without throwing (test-composer-image-markers.js)
- the "[image N]" marker is inserted synchronously at the original cursor position on paste, with the pending item flagged `pending:true` until async decode resolves and assigns the marker; typing elsewhere afterward doesn't relocate the already-placed marker (test-composer-image-markers.js)
- images over 8MB are rejected before decode — no item or marker is added, and an inline banner reads "maximum 8 MB" (test-composer-image-markers.js)
- a 9th attachment is rejected before decode (8-image cap) — no item or marker is added, and an inline banner reads "maximum 8 images" (test-composer-image-markers.js)

### test-input-area.js (input area: layout, auto-grow, capabilities, steer, slash — attachment-integration contracts are under Attachments below)
- after init, the composer's model display and tasks trigger render outside the button row and outside the htmx-swap `#input-status` target, with no standalone task-status-row or model-chip element (test-input-area.js)
- the textarea auto-grows to track its content's scrollHeight as the user types, starting from a fixed baseline height when empty (test-input-area.js)
- textarea auto-grow height clamps at 50% of the viewport height for very long content instead of growing unbounded (test-input-area.js)
- a successful submit clears the textarea and resets its auto-grown height back to the empty baseline (test-input-area.js)
- a failed submit leaves the textarea's text (and its grown height) untouched so the user can retry (test-input-area.js)
- composer send/queue capability attributes exactly mirror the session's server-reported capabilities (never assumed), gating whether the send button actually submits (test-input-area.js)
- a status transition (e.g. idle→active) does not carry over the previous status's cached capabilities — capabilities reset to a disabled/safe default until a fresh capability read resolves for the new status (test-input-area.js)
- a capability refresh that resolves after a NEWER status change has already superseded it is discarded as stale and must not overwrite the newer status's capability state (test-input-area.js)
- a capability refresh that resolves after a same-session hydration has already started a newer generation is likewise discarded as stale rather than overwriting the hydration's capability state (test-input-area.js)
- sending on a session with no currently-open live stream (e.g. after the daemon ended it) transparently reopens exactly one AppWire subscription on send success, so the new turn's events render live without a page reload, using the server's authoritative turn index (test-input-area.js)
- a failed send on a stream-less session does not open a new live stream (test-input-area.js)
- clicking steer with an empty textarea does not POST /steer — it is a focus-only action (test-input-area.js)
- clicking steer with text POSTs to /steer and clears the textarea on success (test-input-area.js)
- if the active turn ends while a steer request is still in flight, the steer button stays disabled once the request settles rather than re-enabling for a turn that's already gone (test-input-area.js)
- "/" as the very first character of an empty textarea opens the command palette (SerfSearch.openWith); "/" anywhere else — mid-text, or with a modifier key held — is always literal and never opens the palette (test-input-area.js)

### CSS/template-token-only (name only, no runtime behavior to port)
- test-composer-layout-css.js
- test-composer-status-compact.js

## Queue (edit / cancel / promote / drain)

### test-queue-and-drain.js
- the queue preview panel starts hidden with depth 0 and an empty list on init (test-queue-and-drain.js)
- when send capability is off and queue capability is on, submitting the composer (Enter) POSTs the text to `/s/<id>/queue` and clears the textarea immediately, ahead of daemon confirmation (test-queue-and-drain.js)
- a second queued message reflects as depth=2 with 2 preview rows once the daemon's `thread/queueChanged` confirms it — the renderer never mirrors queue state locally (test-queue-and-drain.js)
- when the daemon pops the queue head into a turn, the queue preview shrinks to depth=1, dropping the consumed head entry (test-queue-and-drain.js)
- clicking steer while the queue is non-empty posts to `/drain-as-steer` carrying the textarea's current text; the preview hides once the daemon confirms depth=0, and the textarea clears (test-queue-and-drain.js)
- clicking steer with an empty queue but non-empty textarea posts to `/steer` (not `/drain-as-steer`) and clears the textarea (test-queue-and-drain.js)
- clicking steer with both an empty queue and an empty textarea fires no fetch at all (test-queue-and-drain.js)
- Shift+Enter with a non-empty queue triggers the same drain-as-steer path as clicking the steer button (test-queue-and-drain.js)
- when the `/queue` POST fails, queue state is left unchanged (depth 0) and a "queue failed" error banner appears (test-queue-and-drain.js)

### test-queue-edit-cancel.js
- each queue-preview row renders edit, cancel, AND promote buttons, each carrying that row's index in a data attribute (test-queue-edit-cancel.js)
- clicking cancel POSTs `{index, entry_id}` to `/s/<id>/cancel-queued`; the row itself doesn't disappear locally — it waits for the daemon's authoritative `queueChanged`, and surviving rows re-key their index on re-render (test-queue-edit-cancel.js)
- when cancel fails (409 conflict), queue state and the row stay unchanged and a "cancel failed" banner appears (test-queue-edit-cancel.js)
- clicking edit appends the FULL untruncated queued text (not the truncated preview line) to any existing composer text (separated by a blank line), persists it through the same sticky-draft path as real typing, focuses the composer, and cancels the queued copy by index+entry_id — the composer text then survives the queue's re-render once the daemon confirms (test-queue-edit-cancel.js)
- if the edit's underlying cancel fails (409, already consumed), the restored text still stays in the composer (never silently duplicated) with an honest banner ("in composer" + "could not be removed"), and queue state is unchanged (test-queue-edit-cancel.js)
- if canceling a queued entry that had image attachments succeeds, a banner warns the user to re-attach the images since they aren't restored automatically (test-queue-edit-cancel.js)
  - **Implementation note (wave 5 close):** confirmed as implemented — `QueueStrip.tsx`'s edit path (`onRestoreToComposer`) restores text only; dropped image attachments surface via its own `reportRemovedImages` warning toast, never auto-restored. See `w5-integration-wiring-report.md`'s "Seam contradictions found" §1 for why the task brief's own literal wording (merge attachments into pending state too) was not implemented as written.
- an image-only queued entry (no text) has its edit button disabled — clicking it fires no fetch and doesn't touch the composer — while cancel still works normally (test-queue-edit-cancel.js)
- if the daemon's `queueChanged` payload lacks the full `texts` array (old daemon), edit does not restore the truncated preview line as if it were the real message — it no-ops with an "edit not available" banner, while cancel is unaffected (test-queue-edit-cancel.js)
- while an edit/cancel request for a row is in flight, both its edit and cancel buttons are disabled so a double-click can't duplicate the composer text or fire a second conflicting request; both re-enable once the request settles (test-queue-edit-cancel.js)
- after an in-flight cancel settles, the cancel button re-enables but an image-only row's edit button stays disabled (still nothing to recompose) (test-queue-edit-cancel.js)

### test-queue-promote.js
- each queue-preview row has a promote button carrying its row index (test-queue-promote.js)
- clicking promote POSTs `{index, entry_id}` to `/s/<id>/promote-queued`; the row stays until the daemon's `queueChanged` confirms and removes it, and surviving rows re-key their promote index (test-queue-promote.js)
- after a re-render, promoting a row sends that row's CURRENT entry_id, never a stale id carried over from an earlier snapshot (test-queue-promote.js)
- a failed promote (409, queue shifted under the snapshot) leaves queue state and the row unchanged and shows a "promote failed" banner (test-queue-promote.js)

## Pending / Optimistic UI

### test-pending-registry.js
- registering a pending intent renders one `.optimistic-pending` chip containing its text and returns a handle with an id (test-pending-registry.js)
- marking a pending handle failed removes the pending chip and renders a `.optimistic-failed` element showing the failure reason plus a retry affordance (test-pending-registry.js)
- reconciliation matches on whitespace-normalized text and removes the pending chip when the authoritative text matches (test-pending-registry.js)
- reconciliation returns false and leaves the chip in place when the text doesn't match (test-pending-registry.js)
- an image-only `turn/start` submission (empty text, image items) renders a pending chip labeled "[image]" and reconciles against a matching authoritative image-only item (test-pending-registry.js)
- failing an image-only `turn/start` keeps a "[image]"-labeled failed placeholder rather than a blank one (test-pending-registry.js)
- a pending entry not reconciled within its timeout auto-transitions to failed with a "did not confirm" reason (test-pending-registry.js)
- a late-arriving authoritative match still reconciles (removes) an already-failed placeholder (test-pending-registry.js)
- when a failed entry and a live retry share the same text, reconciliation resolves the live pending retry first and leaves the older failed duplicate in place (test-pending-registry.js)
- a `turn/drainAsSteer` pending entry reconciles on ANY matching authoritative event for that method regardless of text, since drain merges multiple queued texts into one (test-pending-registry.js)
- a `turn/queue` pending chip renders as an `<li>` inside the queue list (not the conversation pane), unhides the queue preview, and `tryReconcileQueue` removes chips whose text appears in the daemon's authoritative preview array (test-pending-registry.js)
- `tryReconcileQueue` only removes chips whose text is present in the given preview list, leaving unmatched chips queued (test-pending-registry.js)
- when two pending queue chips share identical text, a single matching preview entry reconciles exactly one of them (one-for-one), not both (test-pending-registry.js)
- the live-retry-over-failed-duplicate preference also applies to queue reconciliation (test-pending-registry.js)
- image-only queue chips use the synthetic "[image]" preview text to match the daemon's authoritative preview (test-pending-registry.js)
- without a `queueList` option, `turn/queue` pending chips fall back to rendering in the conversation pane instead of crashing (test-pending-registry.js)
- clicking a failed chip's retry link removes the failed DOM element BEFORE invoking the `onRetry` callback, so a second failure can't stack duplicate chips (test-pending-registry.js)
- retrying a failed queue or drain-as-steer entry passes its original attachment items back to `onRetry` as a snapshot copy, not the same array reference (test-pending-registry.js)

### test-optimistic-rendering.js
- when the daemon's `turn/steer` RPC returns a JSON-RPC error (e.g. "steer is not available"), the associated pending chip flips to `.optimistic-failed` (test-optimistic-rendering.js)
- a successful `turn/steer` RPC response does NOT itself reconcile the pending chip — it stays `.optimistic-pending` until a later notification triggers reconciliation (test-optimistic-rendering.js)
- with no pending registry installed, `SerfAppwire.steer()` still propagates RPC errors normally — optimistic UI is purely additive (test-optimistic-rendering.js)
- the pending chip is removed once the registry's `tryReconcile` runs with a matching `turn/steer` text (simulating the daemon's STEERING_INJECTED notification), even though RPC success alone didn't reconcile it (test-optimistic-rendering.js)

### test-appwire-queue-reconcile.js (queue-preview + turn optimistic reconciliation, driven through the renderer)
- registering a queued optimistic turn (`turn/queue`) shows a pending chip in `[data-queue-pending-list]` (test-appwire-queue-reconcile.js)
- a `thread/queueChanged` notification whose string preview matches a pending queue chip's text reconciles (removes) that chip (test-appwire-queue-reconcile.js)
- registering an image-only pending user turn (`turn/start`) shows an optimistic pending chip in the conversation (test-appwire-queue-reconcile.js)
- an `item/completed` notification with a matching image-only authoritative user item reconciles the pending `turn/start` chip (test-appwire-queue-reconcile.js)
- multiple pending turns registered before a frame flush accumulate multiple optimistic chips; reconciliation is deferred until `SerfRenderer.flush()`, then runs once per notification IN QUEUE ORDER within that single batched flush, resolving all matching placeholders (test-appwire-queue-reconcile.js)

## Attachments

### test-drag-drop-image.js
- dropping image file(s) onto the composer drop zone queues them as image attachment items (type/mediaType/ArrayBuffer data) and renders one chip per image (test-drag-drop-image.js)
- dropping multiple images at once synchronously inserts one "[image N]" marker per file into the textarea (test-drag-drop-image.js)
- dropping a mix of image + non-image files queues only the images and surfaces one inline rejection banner naming the skipped file(s) (test-drag-drop-image.js)
- dropping only non-image files queues nothing and shows the rejection banner (test-drag-drop-image.js)
- `dragenter` toggles a `.drop-active` class on the drop zone; `dragleave` and `drop` both clear it (test-drag-drop-image.js)
- a drop event is `preventDefault`'d so the browser doesn't navigate to the dropped file (test-drag-drop-image.js)
- selecting image file(s) via the hidden file input queues one item per image and renders chips, matching drop behavior (test-drag-drop-image.js)
- the attach handler defensively sets `accept="image/*"` on a file input missing that attribute, but never overwrites an already-set accept value (test-drag-drop-image.js)
- selecting a non-image file via the picker is rejected (0 items queued) with the same rejection banner as drag-drop (test-drag-drop-image.js)
- clicking the visible attach button programmatically triggers the hidden file input's click, and the attach button itself is keyboard-reachable (real `<button>` or has `tabindex`) (test-drag-drop-image.js)
- a stale rejection banner from an earlier bad drop clears automatically the moment a subsequent paste or drop succeeds cleanly (test-drag-drop-image.js)
- a second rejecting drop REPLACES the banner's message with the latest rejection (naming its own files), never stacking or appending old text (test-drag-drop-image.js)
- rejecting multiple non-image files in one gesture still renders exactly one banner DOM element, not one per file (test-drag-drop-image.js)

### test-paste-image.js
- pasting an image from the clipboard queues one item (type=image, mediaType=image/png, ArrayBuffer data, name "paste-<ts>.png", positive width/height) (test-paste-image.js)
- a text-only paste is left untouched — no attachment is queued, and the event's default (browser text insertion) is not prevented (test-paste-image.js)
- pasting an image alongside text extracts only the image as an attachment, leaving the text to the browser's normal paste handling (event not prevented) (test-paste-image.js)
- the rendered attachment chip shows both the image's filename and its WxH dimensions (test-paste-image.js)
- clicking a chip's remove button empties the pending item and removes the chip from the DOM (test-paste-image.js)
- a pasted non-PNG image (e.g. JPEG) is transparently re-encoded to image/png before being stored (test-paste-image.js)

### test-submit-attachments.js
- `startTurn` base64-encodes an image attachment's binary data into the `turn/start` RPC's `input` array (type=image, mediaType, base64 string, name) — never raw ArrayBuffer/binary in the JSON body (test-submit-attachments.js)
- multiple image attachments each encode to their own `input` entry, in order (test-submit-attachments.js)
- an empty attachments array sends no image entries — the turn stays text-only (test-submit-attachments.js)
- `queueTurn` (`turn/queue`) base64-encodes attachments the same way as `startTurn` (test-submit-attachments.js)
- `drainAsSteer` sends attachments + text as ONE atomic `turn/drainAsSteer` call and registers a single optimistic pending intent that preserves the text and attachment metadata for retry (test-submit-attachments.js)

### test-appwire-attachments-websearch-hydration.js
- non-image input attachments (documents, audio) hydrate from a reloaded thread as labeled file chips, never as broken `<img>` elements (test-appwire-attachments-websearch-hydration.js)
- provider-native `web_search` content hydrates through the existing web_search tool card, showing the query and its result (test-appwire-attachments-websearch-hydration.js)

### test-input-area.js (attachment-integration subset of the input-area harness)
- selecting an image via the composer's file picker queues it as a pending attachment and renders one chip showing its filename (test-input-area.js)
- dropping multiple images at once queues each as a pending attachment and renders one chip per image (test-input-area.js)
- clicking a chip's remove button removes exactly that attachment from the pending queue and its chip from the DOM (test-input-area.js)
- submitting with both text and a pending image attachment POSTs a body with `text` plus an `items` array carrying the image's type/mediaType/base64 `data`/name (test-input-area.js)
- a successful submit clears the pending attachment queue and its rendered chips (test-input-area.js)
- attachments added while an earlier submit is still in flight are excluded from that in-flight request's snapshot, but survive as the new pending queue/chips/draft once that submit succeeds — no data loss, no double-send (test-input-area.js)
- navigating to a different session while an old session's send is still in flight prevents that stale send's eventual completion from rendering a local echo into either the old or the new session's conversation (test-input-area.js)
- submitting while a pasted/picked image is still synchronously pending (async decode unfinished) is blocked — no fetch fires and a "still processing" banner explains why (test-input-area.js)
- submitting an empty textarea with no attachments fires no fetch (test-input-area.js)
- submitting while the send capability is marked unavailable fires no fetch and leaves the send button disabled (test-input-area.js)
- picking a non-image file is rejected (not queued) and shows the shared attachment-error banner naming the offending file (test-input-area.js)

## Drafts

### test-drafts.js
- typing in the composer persists the draft to localStorage under a per-session key (`serf-hub.draft.<sessionId>`) on every input event (test-drafts.js)
- a fresh page load seeded with the same localStorage restores the draft text into the composer (test-drafts.js)
- each session's draft is isolated: switching sessions shows an empty composer rather than another session's draft, typing in one session never touches another's stored draft, and swapping back restores the original session's draft (test-drafts.js)
- a composer with no session id (e.g. the home/new-session view) falls back to a stable shared draft key (`serf-hub.draft.new`) that also persists and restores (test-drafts.js)
- a successful send clears only that session's stored draft (other sessions' drafts are untouched), and the composer stays empty after a subsequent reload (test-drafts.js)
- a successful steer also clears the draft and empties the textarea (test-drafts.js)
- clearing the textarea to empty removes the stored draft; whitespace-only content is likewise never persisted as a draft (test-drafts.js)
- when a composer DOM element is reused across a session swap (element survives, session id attribute changes), rebinding clears the stale prior-session text from the textarea before restoring the new session's draft, writes nothing under the new key until the user types, and a same-session rebind mid-typing preserves the in-progress text (test-drafts.js)
- if the browser's own form-autofill pre-fills a fresh composer with text that verbatim matches another session's stored draft, that foreign text is detected and cleared; text matching no known stored draft is treated as the user's own fresh typing and is preserved (test-drafts.js)

## Ask User

### test-ask-card.js
- a completed-but-unanswered ask_user call leaves a compact transcript anchor (`[data-ask-anchor]`) in place of any interactive card — no retired `.ask-card` renders in the transcript (test-ask-card.js)
- the one interactive response form renders as `[data-ask-response-dock]` owned by `form[data-input-form]`, with a shared footer that is a form-owned sibling of the dock rather than something that scrolls with the question list (test-ask-card.js)
- while an ask is pending, the composer's normal surface and textarea are both hidden AND inert (not just visually hidden), so they can't be typed into or tabbed to (test-ask-card.js)
- the dock renders normal answer options together with the "Something else" (free-text) and "let serf decide" alternatives as one radiogroup; the option marked `recommended` always renders FIRST regardless of input order and is visually tagged, non-recommended options are untagged (test-ask-card.js)
- each alternative (free-text / decide-leaning) option's radio label wraps exactly its own radio control; its editor (free-text input or leaning field) is a DOM sibling in the same row, explicitly labelled via aria-labelledby pointing at that alternative's own option text (test-ask-card.js)
- a fallback button showing the model's literal `if_unanswered` text renders only when `if_unanswered` is present; skip is always offered; the optional note field is visible immediately with no disclosure toggle (test-ask-card.js)
- answer progress is announced through an aria-live="polite" region, and the question's `why` explanation renders verbatim in the dock (test-ask-card.js)
- entering ask-response mode auto-focuses the first usable answer control and announces the mode switch via a concise aria-live="polite" status message; free-answer/decide-leaning/note inputs are all programmatically named via aria-labelledby pointing at real question-text elements (test-ask-card.js)
- activating the free-answer alternative moves focus into its free-text input; a later ask_user call that adds more questions does not steal focus from an answer input currently being edited; resolving the pending ask announces the composer's restoration through the same live region (test-ask-card.js)
- option labels that collide after sanitization (duplicates, or labels colliding with the "free"/"decide" alternative suffixes) still receive globally unique DOM ids, and each alternative's editor resolves its aria-labelledby to its OWN alternative's label, never a colliding regular option (test-ask-card.js)
- two ask_user calls whose call ids collide after sanitization still produce globally unique DOM ids across the dock, and every aria-labelledby reference resolves to a label element within its own owning question (test-ask-card.js)
- when the dock rebuilds (e.g. a new question arrives), focus on any enabled control — option input, free/decide radio, fallback/skip/send button, visible note field — is restored to the equivalent control after the rebuild rather than dropped; a control that becomes hidden or removed by the rebuild is never refocused (test-ask-card.js)
- clicking an already-checked free-text or decide-leaning alternative radio again toggles it off and clears the pending resolution — unlike normal single-select radios, these alternatives are tri-state (test-ask-card.js)
- while an ask is pending, an implicit/leftover form submission is prevented outright — it never sends the composer's stale draft through the normal startTurn path, the ask dock stays active, and the gated composer's draft text is left untouched (test-ask-card.js)
- both a transcript-replay reset and a SESSION_START for a different/replaced session fully tear down an active pending-ask dock (not just clear its internal state) and restore the normal composer (test-ask-card.js)
- no fallback button renders when the question carries no `if_unanswered` text (test-ask-card.js)
- multiple ask_user calls posted with no intervening reply accumulate into a single shared transcript anchor and a single shared response dock, with questions globally numbered in call-then-question posting order rather than restarting per call (test-ask-card.js)
- the user's actual reply (USER_INPUT) removes both the response dock and the compact pending anchor, replacing them with a neutral settled line that names the question and echoes the reply, and restores the normal (unhidden, non-inert) composer (test-ask-card.js)
- clicking the dock's shared Send-answers button composes the picked answers into the `[answers]` text and submits it through the SAME normal startTurn path as typed messages; a successful send resolves the pending ask (dock replaced by settled line) and restores the normal composer (test-ask-card.js)
- a `multi_select` question renders checkboxes rather than radios; checking multiple boxes accumulates all their labels into one resolution, unchecking one removes just that label, unchecking the last clears the resolution entirely (test-ask-card.js)
- pressing Escape while the ask dock is open does not hide, collapse, or discard it (no legacy "collapsed chip"), and preserves any in-progress answer selection (test-ask-card.js)
- on a cold attach (replaying a transcript from scratch), the renderer reconstructs the same state live behavior would: a completed-but-unanswered ask gets both anchor and dock; an ask followed by a reply gets only the settled line; an ack-less/interrupted ask_user call (no END event) produces no anchor, dock, or settled line; a denied ask_user call (END with an error) likewise produces neither anchor nor dock (test-ask-card.js)

### test-ask-compose.js
- the `[answers]` reply format is byte-exact per spec: numbered `N. [Header] → <resolution>` lines for each of the option/decide/skip/free-text resolution kinds, each optionally suffixed with `— note: "..."` (test-ask-compose.js)
- an untouched/unresolved question composes identically to an explicit skip — there is no distinct "unanswered" wire state (test-ask-compose.js)
- the "fallback" resolution composes as `do your stated fallback ("<if_unanswered text>")`, embedding the model's own if_unanswered text verbatim (test-ask-compose.js)
- a multi-select answer joins each picked label as its own quoted string, so a comma inside a label can't be mistaken for the list separator (test-ask-compose.js)
- the quoting helper escapes embedded quotes, backslashes, \n, \t, \r, and other C0 control characters (`\xHH`) exactly like Go's `%q` (test-ask-compose.js)
- through the real dock UI: picking a regular option after free-text was active clears/hides the free-text row, and picking free-text after an option was checked clears the option — exactly one resolution is live per question at a time (mutual exclusion) (test-ask-compose.js)
- the per-question note field attaches to whichever resolution was ultimately chosen — a picked option, an explicit skip, or "you decide" (with its own optional leaning) — and rides along in the composed reply text (test-ask-compose.js)
- after a successful Send, the normal composer returns and both the response dock and its footer are removed from the DOM; a USER_INPUT resolving the ask from another client does the same and leaves a settled transcript line (test-ask-compose.js)
- if the send races and loses (a `conflict` error), the dock tears down, the normal composer returns, and the already-composed `[answers]` text is placed into the visible composer for the user to decide rather than being discarded or auto-retried (test-ask-compose.js)
  - **Implementation note (wave 5 close):** the rewrite preserves any composer draft typed before the question arrived (merges the composed `[answers]` text after it, separated by a blank line) rather than porting legacy's own `dropComposedTextIntoComposer` overwrite behavior (`renderer.js:6238-6245`) — see `w5-integration-wiring-report.md`'s "Seam contradictions found" §2 for the full reasoning. This divergence from the one legacy citation found is approved by Jesse, 2026-07-21.

### test-ask-submit.js
- if the ask was already resolved by the time a stale Send click lands (e.g. a suspended tab waking up, or another client's reply arriving first), the dock re-checks before sending and does not call `startTurn` at all (test-ask-submit.js)
- a `turn/start` Conflict (another client's reply won the daemon's atomic reservation) is never auto-retried: the composed answer text drops into the normal composer, the pending ask and its transcript anchor are discarded rather than settled (no authoritative USER_INPUT confirmed them), and a later acknowledged ask_user call starts a completely fresh pending set instead of merging into the stale conflicted one (test-ask-submit.js)
- sending ask answers calls the exact same `SerfAppwire.startTurn(ref, text)` function the ordinary composer uses — never a parallel ask-only RPC — with the session's real ref and the composed text verbatim (test-ask-submit.js)
- the shared Send button disables the instant a send is in flight, stays disabled even if the dock rebuilds mid-flight (a new question arrives), and re-enables once a non-terminal (retryable) send error occurs (test-ask-submit.js)
- when two ask_user calls are pending with one send in flight, that send's success settles only its own questions in the transcript — a second, later-arrived question keeps its independently-entered answer, note draft, and focus untouched, and remains independently sendable with a payload containing only its own answers (test-ask-submit.js)
- a late-arriving question queued after an earlier send is now in flight still gets torn down by an authoritative USER_INPUT from another client, even though it was never sent locally (test-ask-submit.js)
- an older in-flight send settling does not spuriously re-enable a newer, still-in-flight send button for a different, later ask (test-ask-submit.js)

### test-notifications-ask-awaiting-broadcast.js
- a genuine ask_user-produced SessionAwaiting normalizes to attention level "needs_you" exactly like any other awaiting/warning producer, reaching the browser as an ordinary `serf/attention/changed` broadcast (test-notifications-ask-awaiting-broadcast.js)
- the default "asks" notification loudScope keys off the wire's `askPending` flag (from hubcore's DeriveAttention) rather than any ask-specific branch inside notifications.js itself (test-notifications-ask-awaiting-broadcast.js)

## Escalations

### test-sandbox-escalation.js
- a `serf/sandbox/escalation/requested` notification maps to exactly one `SANDBOX_ESCALATION_REQUESTED` client event carrying `escalationId`, the full `deniedPath`, and the card `kind` (test-sandbox-escalation.js)
- an escalation renders as a HARNESS-framed approval card (never styled as a model message), showing the full denied path and the denied tool name; a file-tool card never shows the shell "partially ran" caveat (test-sandbox-escalation.js)
- clicking Allow posts exactly one resolve call with `{escalationId, approve:true}`; the card only settles to "Allowed once" and drops its controls after the resolve request actually succeeds, never optimistically (test-sandbox-escalation.js)
- clicking Deny posts a resolve with `approve:false` — denial is never silently dropped (test-sandbox-escalation.js)
- a CONFLICT resolve rejection (already resolved elsewhere) renders a distinct terminal "expired" state and never shows a success confirmation (test-sandbox-escalation.js)
- a transport-level resolve rejection (no error code) does not mark the card expired — it re-enables Allow/Deny for retry and shows a transient retry note (test-sandbox-escalation.js)
- a coded-but-non-conflict resolve rejection (e.g. daemon temporarily unavailable) also re-enables the card for retry rather than terminally expiring it — only a genuine conflict is terminal (test-sandbox-escalation.js)

### test-sandbox-escalation-snapshot.js
- entering a session whose thread snapshot carries `pendingEscalations` surfaces those as harness-framed card(s) immediately on hydration (test-sandbox-escalation-snapshot.js)
- a live escalation-requested notification for an id already surfaced from the snapshot does not double-render — it's de-duped by escalation id (test-sandbox-escalation-snapshot.js)
- a live notification for an escalation id NOT in the snapshot still renders its own additional card (test-sandbox-escalation-snapshot.js)
- after a reconnect reset wipes the DOM, re-hydrating with a still-pending escalation in the fresh snapshot re-renders its card — reset must clear the de-dupe set or the card never returns until a full page reload (test-sandbox-escalation-snapshot.js)
- after a reconnect reset, re-hydrating with a fresh snapshot that no longer lists a given escalation (now settled) renders no card for it — settled escalations never reappear (test-sandbox-escalation-snapshot.js)
