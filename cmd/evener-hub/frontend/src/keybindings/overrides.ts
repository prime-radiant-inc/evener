// User override application for the keybinding registry: rebindAction is the
// one primitive the overrides store (src/stores/keybindings.ts) applies each
// validated rule through. A rebind replaces the action's CURRENT binding(s) -
// the default entry and its cross-platform `#mod-twin`, or an earlier
// `#override` - with a single binding that keeps the default's scope and
// policy flags (allowInEditable/allowInModal/ignoreIfDefaultPrevented): a
// rebind changes the chord, never the action's guards. The override chord
// binds exactly what the user wrote - no twin expansion, because user chords
// are platform-explicit (a `$mod` spelling resolves at parse time).
//
// Every throwing path (unknown action, unparseable chord, conflict with
// another action's effective binding) throws BEFORE mutating the registry, so
// a failed rebind never leaves the action half-unbound. The store's semantic
// validation layer (validation.ts) pre-checks the same conditions and skips
// bad rules with warnings, so these throws should never reach startup; they
// exist so a direct caller cannot corrupt the registry either.

import { chordsOverlap, parseChord, serializeChord } from "./chord";
import { DEFAULT_BINDINGS, registerDefaultBindingsForAction } from "./defaults";
import { type BindingInput, GLOBAL_SCOPE, type KeybindingsRegistry } from "./registry";

function defaultInputFor(actionId: string): BindingInput {
  const input = DEFAULT_BINDINGS.find((b) => b.actionId === actionId);
  if (input === undefined) throw new Error(`unknown keybinding action "${actionId}"`);
  return input;
}

/** Removes every binding the action currently has (default, `#mod-twin`,
 * `#override`); the registry-equivalent of an unbound action. */
export function removeActionBindings(registry: KeybindingsRegistry, actionId: string): void {
  const state = registry.getState();
  for (const binding of state.bindings) {
    if (binding.actionId === actionId) state.unregisterBinding(binding.id);
  }
}

/** Applies one override rule: `chord` rebinds the action, null unbinds it.
 * Re-applying the same override is idempotent; a later override replaces an
 * earlier one (last rule wins, matching the wire payload). Throws - without
 * mutating the registry - on an unknown action id, an unparseable chord, or a
 * conflict with another action's effective binding in the same scope. */
export function rebindAction(registry: KeybindingsRegistry, actionId: string, chord: string | null): void {
  const defaultInput = defaultInputFor(actionId);
  if (chord === null) {
    removeActionBindings(registry, actionId);
    return;
  }
  const sequence = parseChord(chord);
  const serialized = serializeChord(sequence);
  const scope = defaultInput.scope ?? GLOBAL_SCOPE;
  for (const existing of registry.getState().bindings) {
    if (existing.actionId === actionId) continue;
    if (existing.scope === scope && chordsOverlap(existing.chord, sequence)) {
      throw new Error(`keybinding conflict: "${serialized}" in scope "${scope}" is already bound by "${existing.id}"`);
    }
  }
  removeActionBindings(registry, actionId);
  registry.getState().registerBinding({
    id: `${defaultInput.id}#override`,
    actionId,
    chord: sequence,
    ...(defaultInput.scope === undefined ? {} : { scope: defaultInput.scope }),
    allowInEditable: defaultInput.allowInEditable,
    allowInModal: defaultInput.allowInModal,
    ignoreIfDefaultPrevented: defaultInput.ignoreIfDefaultPrevented,
  });
}

/** Restores the action's default binding(s) (plus the `$mod` twin), removing
 * any override or unbind. Idempotent on an action that was never overridden.
 * Throws on an unknown action id. */
export function restoreDefaultBinding(registry: KeybindingsRegistry, actionId: string): void {
  defaultInputFor(actionId);
  removeActionBindings(registry, actionId);
  registerDefaultBindingsForAction(registry, actionId);
}
