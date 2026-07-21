# Wave 5 Task 2 report — composer core

Branch `w5-composer`, off T1's tip `e299f4803`. 9 commits. `git diff --stat e299f4803..HEAD --
. ':!cmd/serf-hub/frontend/src/panes/session/composer/' ':!cmd/serf-hub/frontend/src/widgets/dropzone/'
':!cmd/serf-hub/frontend/src/widgets/textarea/' ':!cmd/serf-hub/frontend/src/dev/gallery-sections/dropzone.*'
':!cmd/serf-hub/frontend/dist/'` is empty — zero touches outside the owned manifest plus the two
flagged extensions (see Concerns). Full suite green: 116 test files / 1697 tests (baseline 107
files / 1563 tests), `tsc --noEmit` clean, `biome ci src` clean, `npm run build` clean — each
verified at least twice in a row for stability (one unrelated flake observed and root-caused, see
Verification).

## Commits

| Commit | Unit |
|---|---|
| `99051f651` | Textarea gains onKeyDown/onPaste/ref-forwarding (blocking gap, see Concerns) |
| `13d3df41b` | generic Dropzone widget |
| `5c719fddf` | draft.ts, enterToSendPref.ts, submitRouting.ts (pure logic) |
| `d266fc188` | attachment primitives: limits.ts, textareaMarkers.ts, encodePng.ts |
| `ea761edf1` | useAttachments orchestration hook (first version, DOM-mutating) |
| `9b4de6e6e` | clipboard.ts (imageFilesFromClipboard) |
| `a64aa60d0` | Composer.tsx — full send/steer/queue/drain/interrupt/drafts/attachments wiring |
| `f8d31ed8e` | chip remove-button accessible-name fix |
| `9f5650f6b` | **structural fix**: attachment marker insertion moved off direct DOM mutation onto React state (see TDD evidence) |

## TDD evidence

Every unit: test file written first, run to confirm red (module/export doesn't exist), then
implementation, then green. Verified explicitly for all 12 new/touched test files. Two genuine
bugs caught mid-flight, not just claimed:

- **Cursor-restore-after-strip read-order bug.** `textareaMarkers.ts`'s original `stripMarker` read
  `el.selectionStart` *after* reassigning `el.value` — but assigning a text control's `.value`
  moves its cursor to the end of the new string (spec behavior, reproduced by jsdom), so the read
  saw the reset position, not the actual cursor at strip time. Caught by two tests
  (`textareaMarkers.test.ts`) that assert a specific post-strip cursor position — the legacy
  `composer-attachments.js` has the identical bug, but its own jstest suite never asserts a
  post-strip cursor, so it was never caught there. Fixed by capturing the cursor before mutating
  the value, per this codebase's "fix it when you find it" rule rather than porting the bug.

- **React controlled-input restoration silently reverting a direct DOM mutation.** The first
  `useAttachments` version mutated the composer's controlled `<textarea>` `.value` directly
  (`insertAtCursor`/`stripMarker` took a DOM ref). A new integration test — "picking a file via the
  hidden input attaches it" — kept failing with an empty textarea no matter which event-firing
  technique was used (`fireEvent.change`, manual `Object.defineProperty` + `dispatchEvent`, both
  tried). Bisected empirically with temporary debug instrumentation: the DOM value was correct
  *immediately* inside the change handler, then reset to `""` by the time the handler returned.
  Root cause: React tracks every controlled form element and, specifically around
  "change"-family native events, runs a restoration pass that reverts any DOM value drift it didn't
  cause itself — triggered by the file-picker `<input>`'s own change event, even though the
  textarea it clobbered wasn't the event's target. A click (chip remove) or the textarea's own
  paste event never triggered this, which is why those two paths "worked" first and masked the bug
  until the file-picker path was tested. Fixed structurally, not with a workaround:
  `textareaMarkers.ts` now exposes pure string-splice functions (`insertMarker`/`stripMarker`, no
  DOM parameter at all); `useAttachments` takes a `TextEditor` seam (`read()`/`write()`) instead of
  a raw ref; `Composer.tsx` implements it by routing every marker edit through its own `text`
  state (`setText`) and restoring the native cursor position in a `useLayoutEffect` keyed on `text`
  (runs after React's commit, so it doesn't fight the reconciler). `useAttachments.test.ts` no
  longer needs a real DOM textarea at all — a plain in-memory fake `TextEditor` is a smaller, more
  direct test double for what the hook actually owns.

## Floor-row coverage (parity-m5-composer.md §A/§J/§F/§G, contracts §Composer/§Drafts/§Attachments)

Legend: **shipped** / **superseded** (the legacy mechanic doesn't apply to this architecture,
verified not assumed) / **deferred** (real behavior, not this stream's scope, reason given).

### §A composer basics / contracts §Composer
- Send-vs-queue routing via `deriveSendQueueAvailability` (T1) + `decideSubmitRoute`; submit is a
  no-op on empty content; steer forks classic-steer/drain/no-op via `decideSteerRoute` (kata 0bq1).
  **Shipped**, unit + integration tested.
- Cmd/Ctrl+Enter always submits; bare Enter submits only with `enterToSend` on; Shift+Enter steers
  unless `enterToSend` is on (then literal newline); kbd hints track the same pref. **Shipped.**
- Attachment mid-encode blocks submit/steer with a toast (not a disabled attribute — matches the
  legacy runtime-check shape). **Shipped.**
- Interrupt (Stop) calls `turn/interrupt`, hidden when ended/closed, disabled unless busy +
  capability. **Shipped.**
- `data-capability-*` DOM-attribute caching, tier-2 "trust a possibly-stale live snapshot",
  capability-refresh-race discarding, per-tab "live stream" reopening. **Superseded** — all are
  artifacts of a REST+SSE transport with client-side attribute caching; this architecture derives
  everything fresh from the reactive `ThreadModel` every render over one persistent socket, so
  there is nothing to cache or race.
- Suppression inside a framed side-pane iframe (`isInPane()`). **Superseded**, documented in code —
  this rewrite's multi-pane layout is same-document dockview panels, never `<iframe>` (verified:
  `grep -r iframe src` is empty).
- `/` opens the command palette. **Deferred** — no command palette component exists anywhere in
  this codebase yet (M6/search territory); a documented gap, not a silent omission.
- Steer failing with "no active turn" when clicked with no active turn. **Shipped**, but only
  reachable via the Shift+Enter *keyboard* path now — the button's `disabled` attribute already
  blocks a mouse click in that state (computed fresh every render, unlike legacy's cached
  attribute), so the runtime guard inside the handler is what a keydown-driven call still needs.
  Both paths have their own test.

### §J interrupt — folded into §A above, all shipped.

### §F drafts / contracts §Drafts
- Per-ref localStorage persistence, restore-on-mount, clear-on-success (send/steer/queue/drain
  alike), blank/whitespace never persisted. **Shipped.**
- Cross-session leak guard (`isOtherSessionsDraft`, DOM-element-survives-a-swap detection).
  **Superseded, verified not assumed** — traced through `shell/paneRegistry.ts` ("session" is
  explicitly NOT a singleton pane type) and `shell/DockHost.tsx`'s own comment ("UNMOUNT, NOT HIDE
  ... never re-parents an existing Composer instance onto a different ref"): a mounted Composer's
  `ref` is fixed for its whole lifetime, and a fresh mount's React state starts empty by
  construction, so there is no foreign-session text a lazy initializer could ever observe.
  Documented at length in `draft.ts`'s header and `Composer.tsx`'s mount comment.
- No-session-id fallback key (`serf-hub.draft.new`). **Superseded** — `ComposerProps.ref` is a
  required string; there is no "no session" composer in this architecture (spawn/new-session is a
  wholly separate pane type).

### §G attachments / contracts §Attachments
- Paste (image-only extraction, text-only untouched, mixed paste never prevented), drag-drop (new
  `Dropzone` widget), file-picker, all funneling into one `ingestFiles` path; 8-count/8MB limits;
  monotonic non-reused markers; synchronous marker-insert-then-async-decode with a `pending` flag;
  canvas PNG re-encode (even already-PNG input, stripping color profile/EXIF); base64 conversion at
  the point of encode (not deferred to submit, see design note below). **Shipped**, unit + a
  representative integration test per surface (paste, drop is unit-only via Dropzone + useAttachments
  since both are already proven independently, file-picker has its own integration test after the
  bug above).
- Chip rendering + remove. **Shipped** — dropped the literal 📎 emoji glyph (plain text "name
  (WxH)" instead) per this repo's no-gratuitous-emoji convention and the design system's SVG-icon
  precedent elsewhere; not a floor-row regression since no test asserts the literal glyph character.
- Gesture-version-counter, single-persistent-banner-with-replace-not-append,
  auto-clear-stale-banner-on-next-success. **Superseded by the wave's own binding constraint**
  ("no new banner systems" — decided in T1): every rejection now surfaces as its own
  `useToasts()` push. This eliminates the whole race class the gesture-version counter existed to
  solve (a shared mutable banner node), not just simplifies it.
- ArrayBuffer-until-submit (33% memory-blowup avoidance). **Deliberate deviation** — base64 is
  produced at encode time since `stores/threads.ts`'s `InputAttachment` already requires base64;
  worst case (8 × 8MB) is a non-issue for a single browser tab.
- Non-image attachment hydration from a reloaded thread, `web_search` card hydration. **Out of
  scope** — transcript/historical-item rendering is wave 4's territory, not composer input.

## New widget: Dropzone

`src/widgets/dropzone/index.tsx` — generic, wire-free, caller-supplied `onFiles`. Barrel export
lines for the controller to add to `widgets/index.ts` (not edited myself, per instructions):

```ts
export type { DropzoneProps } from "./dropzone";
export { Dropzone } from "./dropzone";
```

## Verification

- `npx tsc --noEmit`: clean throughout, including after the TextEditor refactor.
- `npx vitest run`: 116 test files / 1697 tests, run 3+ times across the session, green every time
  but one — `panes/session/index.test.tsx`'s Suspense/lazy-load `findByText` (a pre-existing,
  timing-sensitive default-`waitFor`-timeout test I never touched) failed once under the heaviest
  concurrent run, passed in isolation and on an immediate full-suite rerun with the identical file
  set. Zero file overlap with anything I changed; not reproduced again in ~4 subsequent full runs.
  Flagging as an observed flake, not a regression, per the "verify subagent test claims" standard —
  happy to investigate further if the wave controller wants it chased down, but it isn't this
  stream's file to fix (outside the manifest) even if it does need attention eventually.
- `biome ci src` / `npm run build`: clean throughout.
- `git restore cmd/serf-hub/frontend/dist/PLACEHOLDER` after every build, confirmed via `git
  status` before each commit.

## Concerns for the wave controller / T6

1. **Textarea widget extension is outside my literal manifest.** The composer cannot implement
   keyboard shortcuts, paste interception, or cursor-based attachment markers through the T1-shipped
   `Textarea` (no `onKeyDown`/`onPaste` props, no ref forwarding) — a hard blocker for roughly half
   of this task's assigned scope. Extended it additively (new optional props only, zero behavior
   change for existing callers, its own test file updated) rather than duplicating a raw
   `<textarea>` inline (which would violate "widgets only") or leaving keyboard/paste unimplemented.
   No collision risk: T3 (queue strip) and T4 (ask dock) have no reason to touch this widget. Please
   review this specifically since it's a deviation from the stated file list.
2. **Optimistic pending is entirely T3's, by design — this stream calls the plain `threadsStore`
   actions directly** (`send`/`steer`/`queue`/`drainAsSteer`), exactly as instructed ("submit paths
   calling the store actions with toast-on-failure"). The wave plan's own binding constraint says
   optimistic pending must apply uniformly to all four, but T3's mechanism (however it's built)
   can't intercept a call site inside a sibling worktree it doesn't yet know exists. Whoever
   integrates T2+T3 needs to reconcile this — either T3's registry observes `ThreadModel` changes
   reactively rather than wrapping the call site, or T6 wires a shared layer at merge time.
3. **The ask dock (T4) has no way to tell this composer to hide/inert its input row while a
   question is pending** — I left the two slots as bare JSX comments per instructions, but the
   legacy behavior ("real composer surface AND textarea are hidden AND inert while ask is pending")
   needs *some* integration point I couldn't invent without guessing T4's internal state shape.
   Flagging for whoever wires the two subtrees together (T4 itself, or T6).
4. **The controlled-input-mutation bug (see TDD evidence) is a real risk for T3/T4 too** if either
   stream is tempted to mutate a shared controlled input's DOM value directly from outside React's
   own state flow — worth a heads-up since it's a subtle, hard-to-spot-by-inspection class of bug
   (passed code review by eye, passed 2 of 3 test surfaces, only caught by exercising the third).
5. **`/` command-palette shortcut is a documented no-op**, not built — no command palette exists
   anywhere in this codebase yet (M6/search territory).
6. Draft storage key (`serf.composer.draft.v1.<ref>`) and the attachment-rejection toast wording
   were my own design choices (no exact string mandated anywhere) — flag if either needs to match
   something else already decided elsewhere in the wave.
