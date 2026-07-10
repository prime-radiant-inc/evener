# Mobile Safe-Area Dock and Ask-User Response Design

**Date:** 2026-07-09

## Goal

Make the Hub workspace reliable on current iPhone Safari and installed iOS PWA sessions when the on-screen keyboard appears, while making a pending `ask_user` interaction replace the normal composer on every viewport instead of rendering its active controls inline in the transcript.

## Context

The existing mobile stylesheet already declares `viewport-fit=cover`, applies safe-area padding to some mobile chrome, and sizes `#workspace` with `100dvh`. However, the composer uses `position: sticky` inside the workspace flex layout. Under iPhone keyboard and browser/PWA viewport transitions, a sticky child can lose its intended visual-viewport anchor.

The existing renderer appends the interactive ask-user card to `.conversation`, marks it as the agent-question target, and leaves the normal composer active beneath it. A separate needs-you dock merely jumps to the inline card. This creates a scroll-dependent response flow and contradicts the intended interaction: answer the question where the composer normally lives.

## Scope

### In scope

- iPhone-first handling for Safari and installed iOS PWA visual-viewport and safe-area transitions, with standards-based fallback in other browsers.
- One workspace-owned bottom dock that stays above the keyboard and hardware safe area.
- Replacing the normal composer with the canonical pending `ask_user` response controls on desktop, tablet, and phone.
- Preserving existing ask-answer composition, multi-select, notes, fallback, skip, optimistic send, conflict recovery, cross-client settlement, and transcript history semantics.
- Deterministic JSDOM and CSS contract coverage.

### Out of scope

- Server, AppWire protocol, capability model, or API changes.
- A separate modal/bottom-sheet interaction.
- Duplicate inline and docked ask forms.
- Changes to normal composer attachment, queue, steer, or capability behavior outside ask mode.
- Visual browser testing that depends on external services or live providers.

## Architecture

### Workspace shell

`#workspace` remains the page-level flex-column shell:

1. A safe-area-aware workspace header.
2. `.conversation` as the single flexible, vertically scrollable region.
3. `.workspace-input` as a non-scrolling bottom dock with `flex: 0 0 auto` and `margin-top: auto`.

The dock does not rely on `position: sticky` on phones. The outer document remains non-scrolling; transcript scrolling stays inside `.conversation`.

### Usable visual viewport

CSS provides `100vh` and `100dvh` fallbacks. The renderer owns a small lifecycle-bound viewport coordinator that uses `window.visualViewport` when present:

- Listen for `resize` and `scroll`.
- Coalesce updates through `requestAnimationFrame`.
- Set a CSS custom property representing the usable visible height from `visualViewport.height`.
- Install only while a workspace is active and remove listeners when the workspace/session is replaced.

The CSS shell consumes this property with `100dvh`/`100vh` fallback. It does not infer keyboard height or poll.

### Safe areas

- The mobile workspace header and navigation control keep `env(safe-area-inset-top)` clearance.
- Existing connection-banner offsets remain part of the top chrome calculation.
- The bottom dock includes `env(safe-area-inset-bottom)` padding.
- The visual viewport controls usable shell height while safe-area padding controls physical cutouts and home-indicator clearance.

## Ask-user dock mode

### One canonical response form

`.workspace-input` is the only active response surface. When `pendingAsk` exists it enters a semantic response mode, for example `data-response-mode="ask"`.

In normal mode the dock retains the existing composer, attachments, queue preview, status, and controls.

In ask mode:

- The normal composer subtree is hidden and inert.
- The dock displays the canonical existing ask controls: question content, option controls, free-text option, notes, decide/fallback/skip controls, answer count, and Send answers.
- No second copy of the ask state or form is introduced. `pendingAsk` remains the source of truth.
- The form receives a clear accessible label such as “Answer the agent’s questions.”

### Transcript record

When an ask-user call is acknowledged, the conversation receives a compact noninteractive question record/anchor. It retains the question context for transcript history, notification deep links, and “needs you” association, but does not contain a second interactive form.

The existing inline interactive card is replaced by this record and the bottom dock form.

### Lifecycle

- The first acknowledged ask-user call creates the transcript anchor and activates dock ask mode.
- Additional ask-user calls in the same turn append questions to the same dock form in global posting order.
- `pendingAsk` continues to own resolutions, notes, collapsed state only if a non-destructive dock presentation affordance is retained, and send-in-flight state.
- `sendAskAnswers` continues to compose the current byte-exact `[answers]` format and submit through the same `SerfAppwire.startTurn` path.
- On a local successful send, echoed `USER_INPUT` from any client, new turn, session change, or supersession, the ask mode settles and the normal composer returns.
- The transcript anchor becomes the existing neutral settled history line describing the asked questions and response.
- A start-turn conflict never auto-retries. The composed response moves into the restored normal composer exactly as it does today.

### Focus and Escape

- On ask-mode activation, focus moves to the first answer control. If that interaction starts with free text, focus moves to the appropriate text input.
- The transcript anchor and needs-you affordance can jump/focus the dock rather than scrolling to a duplicate interactive card.
- Hidden composer controls cannot receive focus or submit while ask mode is active.
- Escape cannot hide or discard the only answer surface. Any retained compact/dismissed presentation state must preserve an obvious, reachable way to restore the dock without losing draft answers.
- Normal-composer restoration does not steal focus except after the user’s deliberate conflict-recovery flow.

## Accessibility

- The dock exposes a mode-specific accessible name and uses a concise live-region announcement when changing between composer and ask modes.
- The normal tab order continues through transcript content into the bottom dock.
- Ask controls retain native button, radio, checkbox, and input behavior.
- The compact transcript record is noninteractive except for an explicit jump-to-response action, if provided.
- No `aria-hidden` content remains focusable.

## Implementation boundaries

| File | Responsibility |
| --- | --- |
| `cmd/serf-hub/assets/style.css` | Workspace flex/viewport/safe-area contract; non-sticky phone dock; ask-mode dock presentation; responsive rules. |
| `cmd/serf-hub/assets/renderer.js` | Visual viewport coordinator lifecycle; transcript ask anchor; mounting/unmounting the canonical ask response form in the dock; focus and settlement behavior. |
| Hub templates, only if needed | Stable semantic wrappers, dock hooks, and accessible labels. No data-flow changes. |
| `cmd/serf-hub/jstest/*` | Deterministic renderer, ask lifecycle, visual viewport coordinator, and CSS contract tests. |

## Deterministic verification

### Ask lifecycle tests

Extend existing JSDOM coverage to assert:

- A pending ask-user interaction renders its only active controls inside `.workspace-input`.
- The normal composer is hidden/inert and cannot receive focus or submit during ask mode.
- Existing option, multi-select, free-text, note, decide, fallback, and skip choices still produce the exact existing `[answers]` payload.
- Local successful settlement restores the composer and replaces the transcript anchor with the neutral settled history record.
- Another client’s `USER_INPUT` settles the local dock safely.
- A start-turn conflict restores the composer with the composed reply as a draft.

### Viewport coordinator tests

Use a fake `visualViewport` and deterministic request-animation-frame control to assert:

- Resize and scroll events update the usable-height CSS variable.
- Multiple events coalesce to one frame update.
- Session/workspace replacement removes old listeners and does not mutate the new workspace from stale callbacks.

### CSS contract tests

Assert that phone rules preserve:

- Top safe-area handling for workspace header/navigation.
- Bottom safe-area padding for `.workspace-input`.
- `.conversation` as the flexible scroll region.
- `.workspace-input` as non-scrolling fixed flex content rather than a sticky mobile composer.
- A CSS hook for ask response mode without a duplicate inline form contract.

### Commands

Run:

```sh
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-compose.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-mobile-css.js
cd cmd/serf-hub/jstest && ./run-all.sh
go test ./cmd/serf-hub -count=1
git diff --check
```

## Acceptance criteria

1. On current iPhone Safari and installed iOS PWA sessions, the header/navigation clears the OS safe area and remains tappable.
2. With or without the keyboard, the transcript occupies the usable visible space and the bottom dock remains immediately above the keyboard or bottom safe area.
3. A pending ask-user interaction replaces the normal composer on every viewport; there is no competing inline interactive card.
4. Existing ask composition and settlement semantics remain unchanged, including cross-client settlement and conflict draft recovery.
5. Normal composer attachments, queue, steer, and capability behavior return unchanged when ask mode ends.
6. The implementation passes deterministic focused tests, the full Hub JSDOM suite, Hub Go tests, and whitespace checks.
