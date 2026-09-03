# Web UI keybindings

Status: **current** (Phase 2a). The web hub's global keyboard chords are
owned by one module, `cmd/evener-hub/frontend/src/keybindings/`, and one
window-level dispatcher, instead of each component installing its own
`keydown` listener.

## Architecture

Four pieces, all in `src/keybindings/` and all React-free:

- **chord.ts** — the chord AST (`Chord` = required modifiers + optional
  modifiers + key; `KeySequence` = a list of presses). `parseChord` wraps
  tinykeys' `parseKeybinding`, so `$mod` resolves to `Meta` on Apple
  platforms and `Control` elsewhere at parse time. `serializeChord`
  round-trips an AST to a tinykeys string; `formatChord`/`formatSequence`
  render one for humans.
- **registry.ts** — a vanilla zustand store holding three things: the
  **action registry** (action id → run function), the **bindings** (chord →
  action id, plus scope and policy flags), and the **scope stack**. The
  app-wide singleton is `keybindingsRegistry`; tests build their own with
  `createKeybindingsRegistry()`.
- **dispatcher.ts** — the single window-level `keydown` listener. On every
  registry change it rebuilds a tinykeys `createKeybindingsHandler` over the
  active scope set: the scope stack top-down, then the `global` scope.
- **defaults.ts** — the built-in binding map (the six shell chords) and
  `registerDefaultBindings`, which also registers each `$mod` chord's
  cross-platform Meta/Control twin (the pre-dispatcher listeners accepted
  `metaKey || ctrlKey` on every platform, so both work everywhere).

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
preventDefault. A firing binding preventDefaults, then runs the action with
the original event.

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

## Deliberately not here yet

Phase 2a is a behavior-preserving migration only. Later phases of the
webui-keybindings plan add, and this module already leaves room for:

- **Server persistence / user remapping** (Phase 2b) — `Binding.when` is
  stored verbatim and never evaluated yet; bindings are the compiled-in
  defaults only.
- **A shortcuts overlay / cheatsheet UI** (Phase 3).
- **Hold-modifier chord hints** (Phase 4) — `formatChord`/`formatSequence`
  exist for exactly this consumer.
