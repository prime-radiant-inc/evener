# Web UI keybindings

Status: **current** (Phase 2b). The web hub's global keyboard chords are
owned by one module, `cmd/evener-hub/frontend/src/keybindings/`, and one
window-level dispatcher, instead of each component installing its own
`keydown` listener. User overrides persist on the hub and sync to every
paired browser.

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
- **defaults.ts** — the built-in binding map (the six shell chords) and
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
registers a chord action (RailHost, Settings, SelectionQuote) calls it too,
so those components' chords also work when they are mounted without the
shell — their unit tests render them standalone.

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
- **A shortcuts overlay / cheatsheet UI** (Phase 3).
- **Hold-modifier chord hints** (Phase 4) — `formatChord`/`formatSequence`
  exist for exactly this consumer.
- `Binding.when` is still stored verbatim and never evaluated; scope-stack
  membership is the only gating the dispatcher applies.
