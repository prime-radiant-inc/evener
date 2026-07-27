# Cold-start onboarding and mobile Spawn implementation plan

Date: 2026-07-26  
Branch: `wip/kata-cold-start-mobile-spawn`  
Base: `b1b05cc1a`  
Katas: `pc8q`, `rdt8`

## Authority and current assessment

The approved Treatment A spec in `docs/superpowers/specs/2026-07-12-mobile-spawn-form-design.md` is the design authority for `/new`. Mockup 21 choices A+B+C are the authority for optimistic pending chips, the first-turn skeleton, and welcome onboarding. The React implementation and React design system take precedence over the old server-rendered implementation notes in that spec.

The existing React Spawn pane already has the approved auto-growing, seamless textarea, and it preserves the existing `PathField`, `ModelCatalog`, `AdvancedOptions`, attachment, preflight, and start-thread contracts. It does not yet satisfy Treatment A on mobile: configuration is still the desktop row plus collapsed Advanced options, the prompt card owns its actions instead of a pinned mobile action band, and the model/path controls use popovers instead of mobile bottom sheets. The branch readout is intentionally read-only because `src/panes/spawn/startThread.ts` has no branch field in the current wire request.

The existing React session pane already preserves optimistic pending chips through `src/panes/session/pending/PendingChips.tsx` and `src/stores/pendingTurnsStore.ts`. It has no first-turn skeleton and no welcome orientation/examples. `src/widgets/skeleton` is already static, accessible, and non-animated; the session feature will consume that widget rather than add a second loading treatment.

## Exact files and slices

### Slice 1: lock the failing contracts

Add failing tests before production changes:

- `cmd/serf-hub/frontend/src/panes/spawn/MobileSettingRows.test.tsx` will exercise the real mobile row/sheet component behavior: the six rows are in Treatment A order, rows expose readable button names and dialog relationships, an option sheet commits selection and closes, the model sheet uses the existing catalog panel, and the working-directory sheet uses the existing path panel. The tests will assert interaction and accessible state, not CSS/source strings.
- `cmd/serf-hub/frontend/src/panes/spawn/Spawn.test.tsx` will add the mobile Spawn integration contract: the mobile configuration region contains full-width row hooks, the fixed-action-band hook contains the existing attach and primary controls, and the existing auto-grow textarea remains present. The existing desktop behavior tests will be updated only where the approved mobile action label supersedes the stale `Start` assertion; the spawn request, preflight, defaults, attachment, and re-entrancy tests remain unchanged.
- `cmd/serf-hub/frontend/src/panes/session/Session.test.tsx` will add real store/component lifecycle tests for `ref_a`: an untouched empty session has no skeleton; a pending send shows the skeleton and pending chip, remains visible through `turn/started` and the user echo, and disappears on the first authoritative agent item; terminal completion/error and a session ref change remove it. The test will also assert `role="status"`, accessible `Loading`, decorative skeleton lines, and no animation through the existing static Skeleton contract. A test title will name the production break each time.
- `cmd/serf-hub/frontend/src/panes/welcome/Welcome.test.tsx` will assert orientation copy, the three concrete example actions, and that activating an example navigates to `/new?prompt=...` so the real Spawn prefill path receives the prompt. This intentionally follows mockup 21 C and the existing `readUrlPrefill` contract; it does not bypass Spawn’s required working-directory/preflight flow with an invented direct-start request.

Before implementation, run the focused RED commands and record each command and its non-zero result in the implementation commentary and final handoff:

```sh
cd cmd/serf-hub/frontend
npm test -- src/panes/spawn/MobileSettingRows.test.tsx src/panes/spawn/Spawn.test.tsx
npm test -- src/panes/session/Session.test.tsx -t "cold-start|first-turn skeleton|untouched empty"
npm test -- src/panes/welcome/Welcome.test.tsx -t "orientation|example"
```

The expected RED is missing mobile rows/sheets/action-band hooks, missing cold-start rendering, and missing welcome example copy/actions. If a new failure is unrelated to these contracts, run the kata search before opening any issue and stop to investigate the root cause.

### Slice 2: implement Treatment A mobile Spawn surfaces

Add `cmd/serf-hub/frontend/src/panes/spawn/MobileSettingRows.tsx` and `MobileSettingRows.module.css`:

- Render reusable full-width `MobileSettingRow` controls with a 48px minimum hit target, sentence-case sans labels/values, right-side truncation, separator hairlines, and a far-right caret only for interactive rows.
- Render six rows in the approved order: Harness, Model, Working directory, Branch, Reasoning effort, Access mode. Branch uses the current HEAD value as a read-only row and has no misleading picker affordance.
- Render generic option sheets for harness, reasoning effort, and access mode with 48px option buttons, selected-state semantics, and the existing `Sheet side="bottom"` focus trap, Escape, scrim, and focus restoration behavior.
- Render the model sheet with the existing `ModelCatalogPanel`, scoped through Spawn’s existing `loadCatalog` callback. Preserve the required-model state and provide the existing default value only when the daemon allows the default.
- Render the working-directory sheet with the existing `PathFieldPanel`, forwarding completion, recents, fallback directory, validation-independent browse behavior, and last-directory stamping. Do not create a second path picker implementation.

Update `cmd/serf-hub/frontend/src/panes/spawn/Spawn.tsx` and `spawn.module.css`:

- Mount the mobile rows alongside the existing desktop configuration, with CSS selecting the mobile surface at the React shell’s mobile breakpoint and keeping the current desktop configuration behavior intact.
- Add a mobile-specific action-band class to the existing PromptCard control row. Keep one attach button and one primary submit button, make the band fixed to the viewport above `env(safe-area-inset-bottom)`, give it a top edge and raised surface without a shadow, and give the primary button a 52px target and attach at least 44px. Reserve body bottom space so the band never covers the auto-growing field or rows.
- Use the approved user-facing primary label `Spawn` and retain the existing keyboard shortcut, busy state, disabled required-model state, and `handleSpawn` path. Preserve the auto-growing textarea and its existing attachment/paste behavior.
- Keep the existing desktop `ModelField`/`PathField`/Advanced options data flow; mobile sheets call the same callbacks and request functions rather than changing model, harness, launch-config, or thread APIs.

Update `cmd/serf-hub/frontend/src/widgets/promptcard/index.tsx` and `promptcard.test.tsx` only as needed to expose a caller-supplied control-row class. The shared widget must keep its existing field, leading, action order, focus ring, hidden/inert, and no-empty-row behavior; the new prop exists solely to let Spawn pin its already-owned controls on mobile.

The mobile rows and sheets will own their mobile touch-target styling in `cmd/serf-hub/frontend/src/panes/spawn/MobileSettingRows.module.css`; do not add new tokens or duplicate picker markup. Add no changes under `src/shell/rail/**` and no changes to any `Steering*` file.

Green commands for this slice:

```sh
cd cmd/serf-hub/frontend
npm test -- src/widgets/promptcard/promptcard.test.tsx src/panes/spawn/MobileSettingRows.test.tsx src/panes/spawn/Spawn.test.tsx
npm run typecheck
npm run lint
```

### Slice 3: implement the first-turn cold-start skeleton

Add `cmd/serf-hub/frontend/src/panes/session/coldStart.tsx` and focused coverage in `cmd/serf-hub/frontend/src/panes/session/Session.test.tsx`. The component/hook will derive its state from the real `usePendingTurnEntries` and `ThreadModel` store data:

- Gate the treatment to a cold-start session: no prior authoritative conversation history, or the one active first turn that contains only the optimistic/user input. A session with existing history will never get this first-turn treatment for a later message.
- Show only after a send has been registered or the first turn is active and before its first non-user/system item. `turn/started` alone does not remove it, and the optimistic user echo does not remove it.
- Treat the first `agentMessage`, reasoning, tool, or other non-user/system transcript item as the authoritative first frame and remove the skeleton immediately. Do not wait for a text delta.
- Remove it for failed/cancelled/completed terminal first-turn status, pending-send rejection, unmount, or `ref` change. Never show it for a genuinely untouched idle/dormant session.
- Render a realistic three-line turn-shaped wrapper around the existing static `Skeleton`. It will carry no fabricated response text, shimmer, pulse, or live-data claim. The existing Skeleton’s `role="status"`, `aria-label="Loading"`, decorative bars, and static CSS remain the accessibility/motion contract.

Update `cmd/serf-hub/frontend/src/panes/session/Session.tsx` and `session.module.css` to place the cold-start skeleton in the transcript body without disturbing `PendingChips`, the composer footer, dormant empty-state copy, VirtualList, flow overlay, or mobile transcript layout. The skeleton must be the body’s honest interim state, not a fake transcript item in the durable store.

Green commands for this slice:

```sh
cd cmd/serf-hub/frontend
npm test -- src/widgets/skeleton/skeleton.test.tsx src/panes/session/Session.test.tsx -t "cold-start|first-turn skeleton|untouched empty|Loading"
npm run typecheck
```

### Slice 4: add welcome orientation and concrete activation

Update `cmd/serf-hub/frontend/src/panes/welcome/Welcome.tsx` and `welcome.module.css`:

- Keep the existing resume and New session actions.
- Add concise user-facing orientation explaining that a session can read and edit the repository, run commands, and delegate work to helpers.
- Add three concrete example buttons matching mockup 21 C’s intent. Each button navigates through the existing `/new?prompt=` prefill route, so the prompt is real input in the existing Spawn form and the user can start it through the existing primary action/preflight flow. No implementation jargon and no direct wire call from Welcome.

Green commands for this slice:

```sh
cd cmd/serf-hub/frontend
npm test -- src/panes/welcome/Welcome.test.tsx -t "orientation|example|New session"
npm run lint
```

### Slice 5: document and verify the approved surfaces

Add the React/mobile implementation guidance requested by the approved spec to `docs/web-ui/design-system.md`: mobile form rows, 16px editable text, 44px touch targets, pinned action surfaces, auto-growing fields, and bottom-sheet picker usage, all using existing tokens and static honest loading behavior. Do not change the approved spec’s status or add a satisfaction claim unless the browser evidence below proves the implemented React surface.

Run the complete frontend verification from `cmd/serf-hub/frontend`:

```sh
npm test
npm run typecheck
npm run lint
npm run build
npm run layoutguard
npm run overflowguard
```

For the real mobile check, boot an isolated Vite server and headless Chrome through the repository’s existing CDP/overflowguard pattern, set the viewport to exactly 390px wide, open `/new`, and record exact observations for: prompt first in the body, all six rows readable and full-width, no horizontal overflow, action band pinned above the bottom safe-area, textarea growth/shrink, and opening/closing each sheet with Escape and restored focus. If an app harness route cannot seed Spawn without credentials, record the exact blocked condition and use the passing real-component tests plus `npm run overflowguard 390`; do not silently substitute desktop assumptions.

Before the final handoff, inspect `git diff --check`, `git status --short`, the changed-file list, and the final `git log`. Confirm no `src/shell/rail/**` or `Steering*` path changed, no provider credentials/network are needed by default tests, pending chips remain intact, and no skeleton animation was added.

## Commit sequence

1. `Document mobile spawn and cold-start implementation plan` — this plan only; commit before test or production edits.
2. `Add red coverage for mobile spawn surfaces` — failing tests and only the smallest PromptCard test seam if required to make the contract expressible.
3. `Implement Treatment A mobile spawn rows and action band` — mobile rows/sheets, Spawn integration, token-based styles, and green focused verification.
4. `Render honest first-turn cold-start skeleton` — session lifecycle component, styling, and green focused verification; pending chips remain unchanged.
5. `Add welcome orientation and example activation` — user-facing copy, prefill navigation, tests, and green focused verification.
6. `Document and verify mobile spawn and cold-start behavior` — design-system guidance followed by the full verification record. No kata closure, merge, or publish operation.

Every commit will be made after a fresh `git status`; no broad staging command will be used.
