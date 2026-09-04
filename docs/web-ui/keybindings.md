# Web UI keybindings

Status: **current** (Phase 3). The web hub's global keyboard chords are
owned by one module, `cmd/evener-hub/frontend/src/keybindings/`, and one
window-level dispatcher, instead of each component installing its own
`keydown` listener. User overrides persist on the hub and sync to every
paired browser. Phase 3 added the first navigation bindings (session-pane
cycling, transcript scroll) on that infrastructure, plus full keyboard
operation of the AskDock ask-user surface.

## Architecture

Four pieces, all in `src/keybindings/` and all React-free:

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
- **defaults.ts** — the built-in binding map (the six legacy shell chords
  plus the six Phase 3 navigation chords) and
  `registerDefaultBindings`, which also registers each `$mod` chord's
  cross-platform Meta/Control twin (the pre-dispatcher listeners accepted
  `metaKey || ctrlKey` on every platform, so both work everywhere).
  `DEFAULT_BINDINGS` is also the canonical action universe: each entry
  carries the action's user-facing `title`, and the Settings keybindings
  section enumerates it live. `registerDefaultBindingsForAction` and
  `defaultBindingChordsForAction` are the per-action variants the overrides
  layer uses to restore and to simulate restorations.
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

The one wiring point that touches React and the app's stores is
`src/shell/installKeybindings.ts`: an idempotent per-window installer that
registers the default bindings and attaches the dispatcher with the real
predicates injected. AppShell calls it at mount; every component that
registers a chord action (RailHost, Settings, SelectionQuote, and every
Session pane via its transcript-scroll hook) calls it too, so those
components' chords also work when they are mounted without the shell —
their unit tests render them standalone.

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

## Default binding map (as shipped after Phase 3)

Every entry of `DEFAULT_BINDINGS` (`src/keybindings/defaults.ts`), in map
order. Chords are rendered the way `KeyHint`/`formatChord` render them:
`$mod` reads as ⌘ on Apple platforms and Ctrl elsewhere, every other key
name is verbatim. "Policy" lists only the flags that differ from the
defaults (`allowInEditable: false`, `allowInModal: false`,
`ignoreIfDefaultPrevented: true`).

| Chord | Action | Scope | Non-default policy | What it does |
|---|---|---|---|---|
| ⌘K / Ctrl+K | `palette.open` | global | `allowInEditable`, `allowInModal` | Open the command palette |
| ⌘B / Ctrl+B | `rail.toggle` | global | `allowInModal`, `ignoreIfDefaultPrevented: false` | Toggle the sidebar |
| ⌘I / Ctrl+I | `composer.focus` | global | `allowInEditable` | Focus the composer |
| ⌘J / Ctrl+J | `next-needs-you` | global | `allowInEditable` | Go to the next session needing you |
| ⌘' / Ctrl+' | `selection.quote` | global | `allowInEditable`, `allowInModal` | Quote the selection into the composer |
| Alt+ArrowRight | `session.next` | global | — | Focus the next session pane |
| Alt+ArrowLeft | `session.previous` | global | — | Focus the previous session pane |
| Alt+ArrowUp | `transcript.lineUp` | global | — | Scroll the focused pane's transcript up one line |
| Alt+ArrowDown | `transcript.lineDown` | global | — | Scroll the focused pane's transcript down one line |
| Alt+Shift+ArrowUp | `transcript.pageUp` | global | — | Scroll the focused pane's transcript up one page |
| Alt+Shift+ArrowDown | `transcript.pageDown` | global | — | Scroll the focused pane's transcript down one page |
| Escape | `settings.close` | settings | `allowInEditable` | Close settings |

Match permissiveness, per chord (all of it exact preservation of the
pre-dispatcher listeners — Phase 2a's ruling was no behavior change):

- Every `$mod` chord also registers its cross-platform Meta/Control twin,
  so e.g. Ctrl+K fires on macOS exactly like ⌘K. On top of that, the five
  legacy `$mod` chords carry `legacyEitherMod`: the other of Meta/Ctrl is
  an *optional* modifier on both the entry and its twin, so Meta+Ctrl+K
  fires too.
- ⌘K / ⌘I / ⌘J list `[Shift]+[Alt]` as optional modifiers (the legacy
  AppShell listener had no shift/alt guard), so ⌘⇧K, ⌘⌥I, Ctrl+Shift+J
  etc. all fire. This means `composer.focus` and `next-needs-you` keep
  hijacking the browser's DevTools chords (⌘⌥I, Ctrl+Shift+J) exactly as
  the legacy listeners did — see the provisional-status note below.
- ⌘' allows an extra Shift but never Alt (the legacy `!event.altKey`
  AltGr guard); ⌘B is strict — no extra modifiers at all.
- `settings.close`'s Escape lists every modifier optional (the legacy
  listener checked only `event.key === "Escape"`).
- The six Phase 3 Alt chords are strict single-family chords with no
  optional modifiers — Alt+ArrowUp and Alt+Shift+ArrowUp must stay
  distinct bindings (line vs page scroll), which forbids a `[Shift]` on
  the plain chord — and no `$mod` twin. All keep the default
  `allowInEditable: false`, so plain Alt+Arrow keeps its native
  word-move/selection meaning inside inputs and the composer.

Two registered actions have **no default chord**: `transcript.scrollTop`
and `transcript.scrollBottom`. Their handlers exist (each Session pane
registers them), but the Phase 4 keymap decides whether they get a chord
at all. Because `DEFAULT_BINDINGS` is the Settings section's row source
and the override validator's action universe, chord-less actions appear in
neither — an override payload naming one is skipped with an unknown-action
warning.

### Provisional status

The six Phase 3 chords are provisional: Phase 4 owns the comprehensive
keymap and may re-cut any of them (they are registry defaults, remappable
via the Phase 2b overrides store from day one). The ledger records two
known Phase 4 candidates:

1. **Removing the legacy DevTools-chord hijack permissiveness.** ⌘⌥I and
   Ctrl+Shift+J fire `composer.focus` and `next-needs-you` today because
   Phase 2a preserved the legacy listeners' missing shift/alt guards
   exactly. Revisiting that was deferred policy then and is still open.
2. **Deciding chords for `transcript.scrollTop`/`scrollBottom`** — and, if
   they stay chord-less, giving the validator/Settings story for actions
   that exist outside `DEFAULT_BINDINGS`.

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
session pane's transcript; `transcript.scrollTop`/`scrollBottom` jump to
the ends (chord-less for now). The seam is
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
defaults only, never a local fallback copy.

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
  later rule.

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
- **Patch** (used by the future editor): sends `expectedRevision` +
  `{version: 1, rules}`; on success the canonical response is applied; on a
  conflict rejection the store refreshes to `data.current`, sets `conflict`
  and `hubError`, and rethrows.

### Failure posture

Nothing here can crash startup on persisted data. Malformed payloads,
validation failures, unreachable hubs, and reconcile throws all degrade to
warnings or a surfaced `hubError` with the last good (or default) bindings
in place; the `changed`-notification path catches so a reconcile failure
never escapes the client's notification dispatch.

### Settings section

Settings → Keybindings is a **read-only** listing of the effective
bindings: one row per action from `DEFAULT_BINDINGS` (never a
hand-maintained copy), the chord rendered with the `KeyHint` widget from
the live registry, and a "Customized" marker on actions whose effective
bindings differ from the default map (including unbound actions). The
store's `hubSupport`/`hubError`/validation warnings surface as quiet status
or alert text. Editing arrives with the Phase 4 editor.

## Deliberately not here yet

Later phases of the webui-keybindings plan add, and this module already
leaves room for:

- **A keybinding editor** (Phase 4) — the store's `patchOverrides` is the
  write path it will use; the Settings section stays read-only until then.
- **A shortcuts overlay / cheatsheet UI** — planned for Phase 3, not landed;
  Phase 3 as executed shipped the navigation bindings, the AskDock keyboard
  contract, and this map instead.
- **Hold-modifier chord hints** (Phase 4) — `formatChord`/`formatSequence`
  exist for exactly this consumer.
- `Binding.when` is still stored verbatim and never evaluated; scope-stack
  membership is the only gating the dispatcher applies.
