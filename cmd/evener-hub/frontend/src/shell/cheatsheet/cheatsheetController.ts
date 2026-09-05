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
import { CHARACTER_KEY_TRIGGER_BINDING_ID, DEFAULT_BINDINGS } from "../../keybindings/defaults";
import { keybindingsRegistry } from "../../keybindings/registry";
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

/** Enforces "the ? binding is registered exactly while characterKeyTriggers
 * is on AND cheatsheet.toggle is on its default map". An applied override
 * (or unbind) owns the action's whole chord set, so "?" never comes back
 * there - the override is the user's own replacement for both triggers.
 * Idempotent and total: safe to run after any pref or override change. */
export function reconcileCharacterKeyTrigger(): void {
  const registry = keybindingsRegistry.getState();
  const present = registry.bindings.some((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
  if (!prefsStore.getState().characterKeyTriggers) {
    if (present) registry.unregisterBinding(CHARACTER_KEY_TRIGGER_BINDING_ID);
    return;
  }
  if (present) return;
  const input = DEFAULT_BINDINGS.find((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
  if (input === undefined) return;
  const onDefaultMap =
    registry.bindings.some((b) => b.id === ACTIONS.cheatsheetToggle) &&
    !registry.bindings.some((b) => b.actionId === ACTIONS.cheatsheetToggle && b.id.endsWith("#override"));
  if (!onDefaultMap) return;
  registry.registerBinding(input);
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
