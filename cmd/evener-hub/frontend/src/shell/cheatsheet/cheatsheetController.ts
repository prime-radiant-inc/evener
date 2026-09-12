// The cheatsheet overlay's open state and its one piece of registry
// bookkeeping. The store mirrors shell/palette/paletteController.ts: a
// vanilla store the mounted <CheatsheetOverlay/> subscribes to, toggled by
// the cheatsheet.toggle action's handler.
//
// The character-key trigger ("?", the default map's one CONDITIONAL entry -
// keybindings/defaults.ts's CHARACTER_KEY_TRIGGER_BINDING_ID) is live only
// while the characterKeyTriggers pref (stores/prefs.ts, the WCAG 2.1.4
// turn-off, default ON) is on. reconcileCharacterKeyTrigger enforces that as
// an invariant and is subscribed to BOTH sources that can break it:
//
//   - the prefs store (the user flips the toggle), and
//   - the keybindings overrides store (an override restore re-registers
//     EVERY default entry for the action - "?" included - through
//     registerDefaultBindingsForAction, with no knowledge of the pref).
//
// The registry itself is deliberately NOT subscribed: override application
// mutates the registry binding-by-binding, and a synchronous registry
// subscription would observe the half-restored state (base entry present,
// "?" not yet re-registered) and register "?" itself - making the restore's
// own re-registration of the same id throw a duplicate-id error into the
// store's rollback path. Both stores above set their state only after the
// registry mutation is complete, so the reconcile always sees the final
// shape.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { ACTIONS } from "../../keybindings/actions";
import { chordsOverlap, parseChord } from "../../keybindings/chord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID, DEFAULT_BINDINGS } from "../../keybindings/defaults";
import { GLOBAL_SCOPE, keybindingsRegistry } from "../../keybindings/registry";
import { actionDisplayLabel, type ValidationWarning } from "../../keybindings/validation";
import { keybindingsStore } from "../../stores/keybindings";
import { prefsStore } from "../../stores/prefs";

export interface CheatsheetState {
  open: boolean;
}

export const cheatsheetStore = createStore<CheatsheetState>(() => ({ open: false }));

export function openCheatsheet(): void {
  cheatsheetStore.setState({ open: true });
}

export function closeCheatsheet(): void {
  cheatsheetStore.setState({ open: false });
}

/** The cheatsheet.toggle action's handler: ⌘/ opens the overlay and - the
 * binding carries allowInModal so the same chord keeps firing while the
 * dialog is open - toggles it closed again (the p4 plan's "Escape/⌘/
 * toggles closed"). */
export function toggleCheatsheet(): void {
  cheatsheetStore.setState((s) => ({ open: !s.open }));
}

export function useCheatsheetStore<T>(selector: (state: CheatsheetState) => T): T {
  return useStore(cheatsheetStore, selector);
}

/** The store's warnings channel doubles as the surface for the conditional
 * entry's overlap skip (the same list skipped override rules render in).
 * The controller's warning is identified by its reason so the reconcile can
 * replace or clear exactly its own entry. setState fires only on a CHANGE:
 * the reconcile is subscribed to the keybindings store, and a redundant
 * write would re-enter it. */
function setCharacterKeyWarning(conflictWith: string | null): void {
  const state = keybindingsStore.getState();
  const rest = state.warnings.filter((warning) => warning.reason !== "character-key-conflict");
  if (conflictWith === null) {
    if (rest.length !== state.warnings.length) keybindingsStore.setState({ warnings: rest });
    return;
  }
  const message = `the "?" cheatsheet trigger was not registered: chord "[Shift]+?" in scope "global" is already bound by ${actionDisplayLabel(conflictWith)}`;
  if (state.warnings.some((warning) => warning.reason === "character-key-conflict" && warning.message === message))
    return;
  const warning: ValidationWarning = {
    rule: { action: ACTIONS.cheatsheetToggle, chord: "[Shift]+?" },
    reason: "character-key-conflict",
    conflictWith,
    message,
  };
  keybindingsStore.setState({ warnings: [...rest, warning] });
}

/** Enforces "the ? binding is registered exactly while characterKeyTriggers
 * is on AND cheatsheet.toggle is on its default map AND no foreign binding
 * overlaps the chord". An applied override (or unbind) owns the action's
 * whole chord set, so "?" never comes back there - the override is the
 * user's own replacement for both triggers. Idempotent and total: safe to
 * run after any pref or override change. */
export function reconcileCharacterKeyTrigger(): void {
  const registry = keybindingsRegistry.getState();
  const present = registry.bindings.some((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
  if (!prefsStore.getState().characterKeyTriggers) {
    if (present) registry.unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
    setCharacterKeyWarning(null);
    return;
  }
  const input = DEFAULT_BINDINGS.find((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
  if (input === undefined) return;
  // The entry registers with NO validation layer: a chord claimed while the
  // pref was off (the entry was not registered, so nothing conflicted at
  // bind time) must not end up shadowing or shadowed by the built-in. The
  // entry's Shift is OPTIONAL, and chordsOverlap's modifier check treats
  // optionals as allowed on both sides, so this one predicate covers both
  // the bare "?" and the Shift+"?" forms. Only bindings of OTHER actions
  // count - cheatsheet.toggle's own base chord never overlaps "?".
  const sequence = typeof input.chord === "string" ? parseChord(input.chord) : input.chord;
  const scope = input.scope ?? GLOBAL_SCOPE;
  const overlapping = registry.bindings.find(
    (binding) =>
      binding.actionId !== ACTIONS.cheatsheetToggle &&
      binding.scope === scope &&
      chordsOverlap(binding.chord, sequence),
  );
  if (present) {
    // Present does NOT imply checked: a foreign binding registered AFTER
    // "?" (registerBinding's exact-match gate allows bare "?" against
    // "[Shift]+?", and a direct registration bypasses the overrides store's
    // validation) would co-fire with the built-in forever. The foreign
    // binding wins - same precedence as the not-present path - so the entry
    // unregisters and the warning surfaces; the foreign binding's removal
    // re-registers "?" on a later pass and clears the warning.
    if (overlapping !== undefined) {
      registry.unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
      setCharacterKeyWarning(overlapping.actionId);
      return;
    }
    setCharacterKeyWarning(null);
    return;
  }
  const onDefaultMap =
    registry.bindings.some((b) => b.id === ACTIONS.cheatsheetToggle) &&
    !registry.bindings.some((b) => b.actionId === ACTIONS.cheatsheetToggle && b.id.endsWith("#override"));
  if (!onDefaultMap) {
    setCharacterKeyWarning(null);
    return;
  }
  if (overlapping !== undefined) {
    // Do not register; surface the skip. The reconcile is total and
    // subscribed to the overrides store, so removing the overlapping
    // override registers "?" on the next pass and clears the warning.
    setCharacterKeyWarning(overlapping.actionId);
    return;
  }
  registry.registerBinding(input);
  setCharacterKeyWarning(null);
}

/** Installs the reconcile: runs it once against the current state, then on
 * every pref or applied-override change. Returns the disposer. */
export function installCharacterKeyTriggerReconcile(): () => void {
  reconcileCharacterKeyTrigger();
  const unsubPrefs = prefsStore.subscribe(reconcileCharacterKeyTrigger);
  const unsubOverrides = keybindingsStore.subscribe(reconcileCharacterKeyTrigger);
  return () => {
    unsubPrefs();
    unsubOverrides();
  };
}
