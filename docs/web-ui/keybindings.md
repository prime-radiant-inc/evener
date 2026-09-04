# Web UI keybindings

Status: **current** (Phase 4b). The web hub's global keyboard chords are
owned by one module, `cmd/evener-hub/frontend/src/keybindings/`, and one
window-level dispatcher, instead of each component installing its own
`keydown` listener. User overrides persist on the hub and sync to every
paired browser. Phase 3 added the navigation bindings (session-pane
cycling, transcript scroll) on that infrastructure, plus full keyboard
operation of the AskDock ask-user surface. Phase 4a finalized the default
map (strict Mod chords for ⌘K/⌘I/⌘J, new `settings` and transcript
end-jump bindings) and shipped the two discoverability surfaces: the
cheatsheet overlay (⌘/ or `?`) and hold-modifier hint chips.
Phase 4b turned Settings → Keybindings into a capture-based editor over
the synced overrides: pre-flight conflict validation, per-action
unbind/reset (the Settings section below).

## Architecture

Seven pieces, all in `src/keybindings/` and all React-free:

- **chord.ts** — the chord AST (`Chord` = required modifiers + optional
  modifiers + key; `KeySequence` = a list of presses). `parseChord` wraps
  tinykeys' `parseKeybinding`, so `$mod` resolves to `Meta` on Apple
  platforms and `Control` elsewhere at parse time. `serializeChord`
  round-trips an AST to a tinykeys string; `formatChord`/`formatSequence`
  render one for humans.
- **registry.ts** — a vanilla zustand store holding three things: the
  **action registry** (action id → its handlers, in registration order), the
  **bindings** (chord → action id, plus scope and policy flags), and the
  **scope stack**. The app-wide singleton is `keybindingsRegistry`; tests
  build their own with `createKeybindingsRegistry()`.
- **dispatcher.ts** — the single window-level `keydown` listener. On every
  registry change it rebuilds a tinykeys `createKeybindingsHandler` over the
  active scope set: the scope stack top-down, then the `global` scope.
- **defaults.ts** — the built-in binding map (the six legacy shell chords,
  the six Phase 3 navigation chords, and the Phase 4a additions:
  `settings`, `transcript.scrollTop`/`scrollBottom`, and the cheatsheet
  triggers) and `registerDefaultBindings`, which also registers each
  `$mod` chord's cross-platform Meta/Control twin (the pre-dispatcher
  listeners accepted `metaKey || ctrlKey` on every platform, so both work
  everywhere). `DEFAULT_BINDINGS` is also the canonical action universe:
  each entry carries the action's user-facing `title`, and both the
  Settings keybindings section and the cheatsheet overlay enumerate it
  live. One entry is CONDITIONAL: the `?` cheatsheet trigger
  (`CHARACTER_KEY_TRIGGER_BINDING_ID`), registered only while the
  character-key-triggers pref is on (see the cheatsheet section below).
  `registerDefaultBindingsForAction` and `defaultBindingChordsForAction`
  are the per-action variants the overrides layer uses to restore and to
  simulate restorations.
- **overrides.ts** — `rebindAction(registry, actionId, chord | null)`
  replaces an action's current bindings (default + `#mod-twin` + any earlier
  `#override`) with a single `#override` binding that keeps the default's
  scope and policy flags, or unbinds it on `null`. No twin expansion for user
  chords. Every throwing path throws before mutating.
  `restoreDefaultBinding` re-registers the action's defaults.
- **validation.ts** — `validateOverrideRules(rules, registry, platform?)`
  never throws: unknown actions, unparseable chords, reserved
  (browser/OS-claimed) chords, and conflicts each become a typed
  `ValidationWarning` and the rule is skipped.
- **display.ts** — the shared read model for the two binding-list UIs
  (Settings → Keybindings and the cheatsheet overlay):
  `ACTION_DISPLAY_ROWS` (one row per action, in default-map order) and
  `displayBindingsFor`, which picks an action's effective display bindings
  from the live registry (the override alone when one is applied, else
  every non-twin default entry). Both UIs read this module so no
  hand-maintained copy can go stale.

The one wiring point that touches React and the app's stores is
`src/shell/installKeybindings.ts`: an idempotent per-window installer that
registers the default bindings, installs the character-key-trigger
reconcile (the `?` pref gating), and attaches the dispatcher with the real
predicates injected. AppShell calls it at mount; every component that
registers a chord action (RailHost, Settings, SelectionQuote,
CheatsheetOverlay, and every Session pane via its transcript-scroll hook)
calls it too, so those components' chords also work when they are mounted
without the shell — their unit tests render them standalone.

## Precedence

When a `keydown` arrives, in order:

1. **IME composition** (`event.isComposing` / `keyCode === 229`) is ignored,
   blanket.
2. **Scope resolution**: the scope stack, top-down, then `global`. The first
   (highest-precedence) binding for the chord claims it outright — a
   shadowed or policy-blocked binding never falls through to a
   lower-precedence twin, and a codeless synthetic event is matched against
   a code-synthesized view (`fireEvent`-style events carry no `code`, and
   tinykeys' `isKeyboardEvent` gate would otherwise drop them).
3. **Per-binding policy** on the matched binding, in this order:
   - `ignoreIfDefaultPrevented` (default **true**): a keydown another
     handler already claimed is ignored. This is how Settings' Escape
     coexists with a Dialog's own Escape-to-close (OverlayPanel
     preventDefaults; the binding then skips).
   - `allowInModal` (default **false**): an open modal suppresses the
     binding. The injected predicate is
     `paletteStore.getState().open || target.closest('[aria-modal="true"]')`
     — the palette is not an `[aria-modal]` element, so the store half is
     required. `palette.open`, `rail.toggle` and `selection.quote` are
     exempt (`allowInModal: true`) because their pre-dispatcher listeners
     had no modal guard.
   - `allowInEditable` (default **false**): an editable event target
     (`input`/`textarea`/`select`/contenteditable) suppresses the binding.
     `rail.toggle` keeps this suppression so Ctrl+B keeps its emacs
     "cursor back" meaning in native text fields; the other Mod chords
     fire from editable targets, as they always have.

A binding whose action is not (yet) registered does not fire and does not
preventDefault. When an action has handlers, they run in registration order
until one accepts the event: a handler returns `false` to decline (the next
handler is tried) and `true`/`undefined` to accept (iteration stops and the
binding preventDefaults). An event every handler declines is not
preventDefault'd. Multi-instance components rely on this: one SelectionQuote
mounts per session pane, each registering `selection.quote`, and only the
instance holding a captured selection accepts — the multi-instance
equivalent of the per-component listeners the dispatcher replaced.
Phase 3's transcript scroll uses the same contract, keyed on the focused
pane instead of a captured selection (see below).

## Default binding map (final since Phase 4a)

Every entry of `DEFAULT_BINDINGS` (`src/keybindings/defaults.ts`), in map
order. Chords are rendered the way `KeyHint`/`formatChord` render them:
`$mod` reads as ⌘ on Apple platforms and Ctrl elsewhere, every other key
name is verbatim. "Policy" lists only the flags that differ from the
defaults (`allowInEditable: false`, `allowInModal: false`,
`ignoreIfDefaultPrevented: true`). The `?` row is the map's one
conditional entry: it is live only while the character-key-triggers pref
is on (see the cheatsheet section below).

| Chord | Action | Scope | Non-default policy | What it does |
|---|---|---|---|---|
| ⌘K / Ctrl+K | `palette.open` | global | `allowInEditable`, `allowInModal` | Open the command palette |
| ⌘B / Ctrl+B | `rail.toggle` | global | `allowInModal`, `ignoreIfDefaultPrevented: false` | Toggle the sidebar |
| ⌘I / Ctrl+I | `composer.focus` | global | `allowInEditable` | Focus the composer |
| ⌘J / Ctrl+J | `next-needs-you` | global | `allowInEditable` | Go to the next session needing you |
| ⌘' / Ctrl+' | `selection.quote` | global | `allowInEditable`, `allowInModal` | Quote the selection into the composer |
| ⌘, / Ctrl+, | `settings` | global | `allowInEditable` | Open settings (the action id IS the palette's `settings` command id) |
| Alt+ArrowRight | `session.next` | global | — | Focus the next session pane |
| Alt+ArrowLeft | `session.previous` | global | — | Focus the previous session pane |
| Alt+ArrowUp | `transcript.lineUp` | global | — | Scroll the focused pane's transcript up one line |
| Alt+ArrowDown | `transcript.lineDown` | global | — | Scroll the focused pane's transcript down one line |
| Alt+Shift+ArrowUp | `transcript.pageUp` | global | — | Scroll the focused pane's transcript up one page |
| Alt+Shift+ArrowDown | `transcript.pageDown` | global | — | Scroll the focused pane's transcript down one page |
| Alt+Home | `transcript.scrollTop` | global | — | Jump the focused pane's transcript to the top |
| Alt+End | `transcript.scrollBottom` | global | — | Jump the focused pane's transcript to the bottom |
| Escape | `settings.close` | settings | `allowInEditable` | Close settings |
| ⌘/ / Ctrl+/ | `cheatsheet.toggle` | global | `allowInEditable`, `allowInModal` | Open/close the keyboard shortcuts overlay |
| ? | `cheatsheet.toggle` | global | conditional on the character-key-triggers pref | Open/close the keyboard shortcuts overlay |
| Escape | `cheatsheet.close` | cheatsheet | `allowInEditable` | Close the keyboard shortcuts overlay |

Match permissiveness, per chord. The legacy chords preserve the
pre-dispatcher listeners exactly except where Phase 4a deliberately
tightened them — called out first:

- **Phase 4a behavior change: ⌘K / ⌘I / ⌘J are now STRICT.** The 2a map
  had carried the legacy AppShell listener's missing shift/alt guard as
  `[Shift]+[Alt]` optionality, so ⌘⌥I fired `composer.focus` and
  Ctrl+Shift+J fired `next-needs-you` — hijacking the browser's DevTools
  chords. Phase 4a dropped that optionality
  (`docs/superpowers/plans/2026-09-04-webui-keybindings-p4-plan.md`,
  Design decision 1): ⌘⌥I, Ctrl+Shift+J, and ⌘⇧J now revert to the
  browser. The `legacyEitherMod` either/both-Meta-Ctrl permissiveness
  stays.
- Every `$mod` chord also registers its cross-platform Meta/Control twin,
  so e.g. Ctrl+K fires on macOS exactly like ⌘K. On top of that, the five
  legacy `$mod` chords carry `legacyEitherMod`: the other of Meta/Ctrl is
  an *optional* modifier on both the entry and its twin, so Meta+Ctrl+K
  fires too.
- ⌘' allows an extra Shift but never Alt (the legacy `!event.altKey`
  AltGr guard); ⌘B is strict — no extra modifiers at all.
- ⌘, (`settings`) and ⌘/ (`cheatsheet.toggle`) are new chords with no
  legacy listener to match: strict `$mod` with the cross-platform twin
  only, no `legacyEitherMod`.
- `?` lists Shift as optional because tinykeys rejects an event carrying
  any modifier the binding does not name, and every common layout types
  `?` WITH Shift held — a bare `?` binding would never fire. Display
  drops optional modifiers, so the row still reads `?`.
- `settings.close`'s Escape lists every modifier optional (the legacy
  listener checked only `event.key === "Escape"`); `cheatsheet.close`'s
  Escape is the same.
- The eight Alt chords are strict single-family chords with no
  optional modifiers — Alt+ArrowUp and Alt+Shift+ArrowUp must stay
  distinct bindings (line vs page scroll), which forbids a `[Shift]` on
  the plain chord — and no `$mod` twin. All keep the default
  `allowInEditable: false`, so plain Alt+Arrow and Alt+Home/End keep
  their native word-move/selection/caret meaning inside inputs and the
  composer.

The map is final as of Phase 4a: the provisional ledger Phase 3 carried
is closed — the DevTools-chord hijack permissiveness is gone (above) and
`transcript.scrollTop`/`scrollBottom` have their chords. Every binding
stays remappable through the hub-synced overrides store (Persistence
below); Settings → Keybindings edits them directly (the Settings section
at the end of this document).

## Phase 3: session-pane cycling

`session.next` / `session.previous` (Alt+ArrowRight / Alt+ArrowLeft) cycle
focus through the workspace's **session** panes — panes of
`type === "session"` only; transcript/doc/tasks panels are not cycling
targets. The semantics live in `src/shell/sessionCycle.ts`
(`cycleSessionPane`), against the store; AppShell's handlers are one line
each:

- Order is `workspaceStore.panes` order, wrapping at both ends.
- Fewer than two session panes open is a no-op (cycling one pane onto
  itself would be motion without movement).
- When the focused pane is not a session (settings, a doc, nothing
  focused), `next` lands on the FIRST session pane and `previous` on the
  LAST — the `nextNeedsYouRef` precedent for "current not in the list".
- The handlers always **claim** the chord (they never decline), even when
  cycling no-ops: Alt+ArrowLeft is the browser's Back shortcut, and
  declining it would navigate the SPA's history underneath a user with a
  single session open.
- Desktop only: AppShell registers nothing on mobile (RailHost's
  `rail.toggle` no-registration pattern), so the bindings are inert there.

## Phase 3: transcript scroll

`transcript.lineUp/lineDown` (Alt+ArrowUp/Down) and
`transcript.pageUp/pageDown` (Alt+Shift+ArrowUp/Down) scroll the focused
session pane's transcript; `transcript.scrollTop`/`scrollBottom` (Alt+Home
/ Alt+End since Phase 4a) jump to the ends. The seam is
`src/panes/session/transcript/flow/useTranscriptScrollKeys.ts`, mounted
once per Session pane:

- **Per-pane multi-instance registration.** Every mounted Session pane
  registers its own handlers for all six `transcript.*` actions on the
  shared registry. Arbitration is the registry's multi-instance contract:
  a handler returns `false` (declines) unless
  `workspaceStore.focusedPaneId` is its own pane, so only the focused
  pane's transcript scrolls. Unmount disposes only that pane's handlers.
- **No new scroll math** — the mechanisms are the transcript flow stack's
  own. Line steps write the virtualizer scroll element's `scrollTop`
  directly by `TRANSCRIPT_LINE_SCROLL_PX` (40px), the same adjustment
  `useTranscriptScroll`'s anchor-restore paths use; page steps use
  `± 0.9 * clientHeight` (`TRANSCRIPT_PAGE_SCROLL_RATIO`). `scrollTop` is
  the virtualizer's `scrollToIndex(0, { align: "start" })`; `scrollBottom`
  is the hook's own `jumpToBottom` (error-anchor aware, pill clearing) —
  the exact action the NewContentPill click target runs.
- A pane whose VirtualList hasn't mounted (a dormant session renders
  EmptyTranscript — no scroll element) declines, so such a keydown is not
  preventDefault'd.
- Mobile registers nothing; with no handler the bindings are inert there.

## Phase 3: AskDock keyboard contract

The AskDock ask-user answering surface
(`src/panes/session/composer/askDock/AskDock.tsx`) is fully
keyboard-operable. Its keys are **component-internal** `keydown` handlers,
not registry bindings, so they are not remappable via the Phase 2b
overrides store:

- Every rendered control is a native element: radios (single-option
  questions, arrows move+select inside the group), checkboxes
  (multi-option), the free-text and note inputs, and the footer's primary
  `<button>`. A multi-question batch's tab strip is ARIA tabs with a
  roving tabindex and automatic activation (Arrow keys move and select;
  Home/End jump the ends).
- **Mod+Enter anywhere in a batch** invokes the footer primary action
  (Send, or advance in the question walk). Bare Enter does the same only
  from the free-text answer input. Both are IME-guarded
  (`isComposing`).
- **Alt+PageDown / Alt+PageUp** jump focus between batches directly,
  wrapping at both ends, landing on the target batch's selected tab when
  it has a tab strip, else its first answer control. The chord is strict
  Alt-only (an extra modifier or an IME composition lets the key keep its
  other meaning); with fewer than two batches the event is left alone, not
  claimed. Alt+Arrow* was rejected for this because the transcript-scroll
  bindings own it, and bare PageUp/PageDown keep their native meaning (the
  dock is its own `overflow-y: auto` scroller).
- **Escape is a deliberate no-op** — the component installs no Escape
  handler at all. `docs/web-ui/parity/parity-m5-composer.md:120` documents
  the legacy behavior: the dock is the one canonical response surface and
  there is no alternate "collapse" state to escape to. AskDock.test.tsx
  pins the no-op.
- **Focus return after send**: when a send empties the dock, focus returns
  to the composer through `composerFocus.ts`'s `requestComposerFocus` seam
  (the request survives until the composer input row's hidden/inert lifts,
  which this send just caused). When batches remain, the composer input
  row is still hidden/inert, so focus moves to the first remaining batch's
  entry control instead. A failed send keeps focus in the intact dock; a
  stale outcome is a no-op.

## Phase 4a: cheatsheet overlay

`cheatsheet.toggle` opens a modal "Keyboard shortcuts" overlay
(`src/shell/cheatsheet/CheatsheetOverlay.tsx`) listing every action with
its EFFECTIVE chord, grouped Sessions / Transcript / Composer / General
(anything the grouping doesn't name — including a future action — lands
in General, so a new action can never silently vanish). Both the row list
and the chords are live reads (`keybindings/display.ts` over the
registry), so hub-synced overrides and unbound actions render truthfully
— an action with no effective binding shows "Unbound".

Triggers:

- **⌘/ (Ctrl+/ elsewhere)** — the primary trigger, an ordinary default-map
  binding for `cheatsheet.toggle`. `allowInEditable` (not a printable
  character) and `allowInModal`, so the same chord also toggles the
  overlay CLOSED while it is open.
- **?** — the secondary trigger, and the default map's one CONDITIONAL
  entry (`CHARACTER_KEY_TRIGGER_BINDING_ID`). It is registered exactly
  while the **Character-key shortcuts** pref
  (`prefsStore.characterKeyTriggers`, browser-local localStorage, default
  ON) is on — the WCAG 2.1.4 turn-off for single-character shortcuts. The
  pref surfaces as a Switch row at the top of Settings → Keybindings;
  turning it off leaves every shortcut on a modifier chord. Because `?` is
  a printable character it never fires from an editable target or over a
  modal. The reconcile (`cheatsheetController.ts`) subscribes to both the
  prefs store and the overrides store: an applied override (or unbind)
  owns the action's whole chord set, so `?` never reappears on an
  overridden `cheatsheet.toggle` — the override is the user's own
  replacement for both triggers.
- **Close**: Escape (the OverlayPanel's own handler claims it first
  whenever focus is inside the dialog; the scope-gated `cheatsheet.close`
  binding is the window-level backstop) or ⌘/ again.

Desktop-only: AppShell mounts the overlay only off mobile, so no action
is registered on a touch viewport and the trigger chords stay inert there
(RailHost's `rail.toggle` no-registration pattern). The `?` reconcile
installs with the dispatcher, not the overlay, so the pref invariant — and
the Settings section's customized-marker comparison — holds on mobile too.

## Phase 4a: hold-modifier hints

Holding the primary modifier ALONE — ⌘ on Apple platforms, Ctrl elsewhere
— for ~400ms fades hint chips in over the bound affordances
(`src/shell/holdhints/`): the palette trigger, the rail toggle, the
session tabs (one chip carrying both cycling chords), and the composer.
Each chip shows the action's effective chord from the live registry (the
same `display.ts` read the overlay and Settings make), so overrides show
truthfully and an unbound action renders no chip. Chips anchor to the real
elements at show time via their data attributes — never
absolutely-positioned guesses — so an affordance that is not mounted (the
rail's toggle exists in exactly one of its two states) renders no chip.

Release hides the chips, and so does every other cleanup path: window
blur, `visibilitychange`, any keydown that is not the tracked modifier (on
macOS a chord's keyup may never arrive, so the keydown itself is the
hide), and a 10s hard-timeout backstop. A stuck visible state must be
impossible. The listeners are observers only — they never preventDefault
or stopPropagation, so the dispatcher and every other keydown consumer see
exactly the events they would see without this module. Under
`prefers-reduced-motion` the chips appear instantly (no fade).
Desktop-only by AppShell's mount gate: a touch viewport installs no
listeners and renders no chips.

## Registering an action and a binding

```ts
import { ACTIONS } from "../keybindings/actions";
import { keybindingsRegistry } from "../keybindings/registry";
import { installKeybindings } from "./installKeybindings";

// In a component effect; the disposer runs on unmount.
useEffect(() => {
  installKeybindings();
  return keybindingsRegistry.getState().registerAction(ACTIONS.myAction, (event) => {
    // ...
  });
}, []);
```

A new chord is a `registerBinding` call (or, for a built-in, a new entry in
`DEFAULT_BINDINGS`):

```ts
keybindingsRegistry.getState().registerBinding({
  id: "my-feature.my-chord",      // unique; duplicates throw
  actionId: ACTIONS.myAction,
  chord: "$mod+Shift+P",          // tinykeys syntax; [Alt] = optional modifier
  scope: "my-pane",               // optional; defaults to "global"
});
```

Registering the same chord twice in the SAME scope throws; the same chord in
a DIFFERENT scope is allowed and shadowed deterministically by scope-stack
order. A pane that owns a scope pushes it while open and pops it on unmount
(`pushScope` returns an idempotent disposer) — Settings.tsx is the template:
it pushes `SETTINGS_SCOPE` and registers `settings.close` in one effect.

## Testing conventions

- Module tests (`src/keybindings/*.test.ts`) use a private
  `createKeybindingsRegistry()` and attach a dispatcher to `window`
  directly. jsdom resolves `$mod` to `Control` on every host, so module
  tests press Mod chords with `ctrlKey`.
- App-level tests press chords the way the existing suites do
  (`user.keyboard("{Meta>}k{/Meta}")`, `fireEvent.keyDown(...)`) — the
  Meta/Control twin registration makes those exercise the same path
  production does. Include `code` or don't; both match.
- Migration/behavior tests for the shell chords live in
  `src/shell/keybindingsMigration.test.tsx`. Assert observable effects (the
  palette opened, `requestComposerFocus` was called, the pane closed), not
  dispatcher internals.
- Determinism: no timers, no network, no localStorage in the module itself.
  A component's action registration/scope push must be undone by its effect
  cleanup so the next test in the file starts clean.

## Persistence (Phase 2b)

Overrides are stored **on the hub**, not in the browser. There is no
localStorage layer: an unsupported or unreachable hub means built-in
defaults only, never a local fallback copy. Live-registry posture follows
the same claim: when support resolves to **unsupported** (the feature set
is known and does not advertise keybindings) any applied overrides are
un-applied through the same atomic unwind the reconcile uses, because the
settings section tells the user the built-in defaults are in effect. A
supported hub that is merely **unreachable** (transient disconnect) keeps
its overrides firing — the rows show them read-only until the hub returns.

### Server side

`cmd/evener-hub/internal/hubcore/keybindings_store.go` keeps one snapshot
`{version, revision, config}` at `<HubStateRoot>/keybindings/state.json`,
written temp-file + rename + fsync. Three appwire methods
(`appwire/keybindings.go`, handlers in `cmd/evener-hub/app_rpc_keybindings.go`):

- `evener/settings/keybindings/get` → the canonical payload.
- `evener/settings/keybindings/patch` — optimistic concurrency: params are
  `{expectedRevision, config}`; a stale `expectedRevision` is rejected with
  appwire `CodeConflict` (-32013) whose `data` carries
  `evenerErrorInfo: "conflict"` and `current`, the server's present payload.
  A no-op patch returns current without touching disk.
- `evener/settings/keybindings/changed` — broadcast to all clients, only
  when the revision actually changed.

The feature is advertised as `features.keybindingsSettings`; an older hub
simply omits it.

### Payload contract

```json
{ "version": 1, "revision": 3, "rules": [ { "action": "palette.open", "chord": "Control+P" } ] }
```

`rules` is the whole override set, not a delta: `chord` (a tinykeys string)
rebinds the action, `chord: null` unbinds it, and an action absent from
`rules` is on its defaults. Revision 0 with empty rules is the shipped
default.

### Validation split

- **Server — structure only.** It does not know the frontend action
  registry, so it enforces the shape: strict decode (unknown fields and
  trailing JSON rejected), `version == 1`, required per-rule `action`/`chord`
  keys, non-empty/non-whitespace action and chord strings, revision
  optimism. A state file that fails the raw-shape re-check on load falls
  back to shipped defaults and the store goes read-only (patches are
  rejected) until the file is fixed.
- **Frontend — semantics** (`src/keybindings/validation.ts`): action ids
  against the default map, chord parseability, the survey's platform-split
  never-use list (chords the browser will never deliver to the page), and
  conflicts against a simulation of the FINAL effective map the payload
  would produce. The simulation includes the implied default-restoration of
  every currently-overridden action the payload drops, iterated to a
  fixpoint; when a rule's chord collides with a restoration, **the restore
  wins** — the rule is skipped with a `conflict` warning and its action
  falls back to its own defaults, so every action stays bound. A chord freed
  earlier in the same payload (rebind-away or unbind) can be claimed by a
  later rule. The restored set mirrors the character-key pref: while the
  pref is off the live registry has no `?` binding, so the simulated restore
  does not claim one either — resetting `cheatsheet.toggle` cannot
  phantom-conflict with a user chord that overlaps `?`.

### Apply flow

`src/stores/keybindings.ts` owns sync. Applied state lives in the registry,
not the store; the store carries `hubSupport`, `revision`, the validated
`overrides`, `warnings`, `hubError`, and `conflict`.

- **Startup**: once the connection is ready and the feature is advertised,
  `get` → structural re-check of the wire payload (`fromWireOverrides`:
  version 1, safe-integer revision ≥ 0, well-formed rules) → semantic
  validation → reconcile.
- **Changed**: the same payload path; revisions older than the store's are
  ignored.
- **Reconcile** is a delta: only actions whose effective chord changed are
  rebound, two-phase (strip every changed action's bindings, then
  re-establish each via `rebindAction` or `restoreDefaultBinding`) so a
  payload moving a chord between actions never trips a transient conflict.
  Untouched actions keep their exact binding objects — in-flight dispatcher
  state is never torn down. The mutation is atomic: a throw rolls the
  registry back to a pre-reconcile snapshot, and the store's `revision`
  advances only after a successful apply, so a failed payload stays
  retryable (a later `changed` with the same revision is not eaten by the
  stale guard).
- **Patch** (the editor's write path): sends `expectedRevision` +
  `{version: 1, rules}`; on success the canonical response is applied; on a
  conflict rejection the store refreshes to `data.current`, sets `conflict`
  and `hubError`, and rethrows. The store rejects a patch while any refresh
  is in flight (`hubLoading`): the in-flight payload can land at any
  revision, and a concurrent PATCH's `expectedRevision` would race it.

### Failure posture

Nothing here can crash startup on persisted data. Malformed payloads,
validation failures, unreachable hubs, and reconcile throws all degrade to
warnings or a surfaced `hubError` with the last good (or default) bindings
in place; the `changed`-notification path catches so a reconcile failure
never escapes the client's notification dispatch.

### Settings section (Phase 4b editor)

Settings → Keybindings edits the synced overrides, not just lists them.
Whenever the hub advertises `features.keybindingsSettings`
(`hubSupport === "supported"`) every row is editable; an `unknown` or
`unsupported` hub keeps the read-only listing and its status text — the
overrides layer is hub-only by design, so there is nothing to edit
against. The rows themselves are unchanged from Phase 4a: one per action
from `DEFAULT_BINDINGS` via `keybindings/display.ts` (never a
hand-maintained copy), the chord rendered with the `KeyHint` widget from
the live registry, a "Customized" marker on actions whose effective
bindings differ from the default map (including unbound actions; the `?`
entry's pref-conditional registration is not itself a customization), and
the **Character-key shortcuts** Switch row at the top owning the WCAG
2.1.4 pref.

- **Capture.** Clicking a row's chord ("Change the shortcut for {title}",
  or "Set a shortcut" when unbound) swaps the row's controls for a focused
  capture box showing "Press new shortcut…". Held modifiers echo live as
  `KeyHint` chips; the first non-modifier press records the chord. Plain
  Enter saves. Plain Escape ALWAYS cancels the capture — bare Escape
  cannot be assigned through the editor (Escape-as-cancel is the
  keybinding-editor convention), so the two built-in Escape bindings
  (Close settings, Close the keyboard shortcuts overlay) are restored via
  **Reset to default** rather than re-captured. Enter/Escape WITH a
  modifier still record as chords (Shift+Escape is bindable). Clicking
  away cancels, whether or not the click target can take focus. The
  capture box preventDefaults and stopPropagations every keydown, so the
  dispatcher and `settings.close`'s own Escape stay inert while capturing.
  IME composition keydowns are ignored with the dispatcher's own guard
  (`isComposing` / `keyCode === 229` / `Process` / `Unidentified`), so a
  composition or its commit Enter can never record or save. A save is a
  hub round trip that can outlive the box: a click-away cancel mid-save
  bumps a per-capture generation token, and the stale save's resolution
  applies only while its generation is current — it cannot close or
  repaint a capture the user reopened before it landed.
- **Single-press only.** Capture records one press — no multi-press
  sequences — matching the all-single-press default map (multi-press
  conflict checking is deliberately coarser). Bare-character chords (a
  plain "A") CAN be authored; the character-key pref governs only the
  built-in `?` trigger, not user-authored character chords, so bind bare
  characters only if you know you mean it.
- **Conflict semantics.** Every save is pre-flight validated in the store
  — the same `validateOverrideRules` simulation over the final effective
  map the reconcile path uses — BEFORE any hub request. A chord already
  held by another binding in the same scope is rejected inline with a
  message naming the chord, the scope, and the action holding it; the
  capture box stays open so another chord can be tried, and nothing
  reaches the hub. Reserved (browser/OS-claimed) and unparseable chords
  reject the same way. A passing save sends the FULL replacement rule set
  with `expectedRevision` optimistic concurrency (the apply flow above); a
  revision conflict refreshes to the server's current payload.
- **Unbind / Reset.** "Unbind" (bound actions) writes a `chord: null`
  rule; "Reset" (actions carrying an override) drops the action from the
  payload, restoring its defaults through the reconcile's
  `restoreDefaultBinding`. Failures surface inline under the row.
- **cheatsheet.toggle.** The ⌘/ base chord edits like any other. The `?`
  character-key entry renders read-only with a note while it is
  registered: it follows the character-key pref and the
  cheatsheetController reconcile, not this editor — and because an
  override owns the action's whole chord set, rebinding or unbinding the
  base chord replaces `?` too.

The store's `hubSupport`/`hubError`/validation warnings surface as the
same quiet status or alert text as before.

## Deliberately not here yet

- `Binding.when` is still stored verbatim and never evaluated; scope-stack
  membership is the only gating the dispatcher applies.
- Multi-press (sequence) chords — the default map is single-press
  throughout and the editor's capture records one press, by design.
