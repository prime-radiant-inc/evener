// Display sourcing for the two binding-list UIs (the Settings keybindings
// section and the cheatsheet overlay): WHICH actions to list, in what order,
// and which of an action's registered bindings is the one to show. Both
// consumers read this module so the list can never drift into two
// hand-maintained copies - the survey's stale-HELP_ROWS lesson, named as a
// binding constraint in docs/superpowers/plans/2026-09-04-webui-keybindings-p4-plan.md.
//
// Deliberately React-free and store-free like the rest of src/keybindings/:
// the registry's bindings array and the characterKeyTriggers pref value are
// passed in by the caller.

import { serializeChord } from "./chord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID, DEFAULT_BINDINGS, defaultBindingChordsForAction } from "./defaults";
import type { Binding } from "./registry";

export interface ActionDisplayRow {
  actionId: string;
  title: string;
}

/** One row per action, in default-map order. Module-level because it derives
 * entirely from the compiled-in default map; the effective chord each row
 * shows is read from the live registry at render. */
export const ACTION_DISPLAY_ROWS: readonly ActionDisplayRow[] = (() => {
  const seen = new Set<string>();
  const rows: ActionDisplayRow[] = [];
  for (const input of DEFAULT_BINDINGS) {
    if (seen.has(input.actionId)) continue;
    seen.add(input.actionId);
    rows.push({ actionId: input.actionId, title: input.title });
  }
  return rows;
})();

/** The bindings to display for an action, as the effective chord list: the
 * override alone when one is applied (it replaces the action's whole chord
 * set), else every non-twin default entry - the platform base entry plus any
 * extra default chords of the same action (cheatsheet.toggle's "?" alongside
 * "$mod+/"). The `#mod-twin` entries are the same chord's cross-platform
 * spelling, never shown. Empty when the action is unbound. */
export function displayBindingsFor(bindings: readonly Binding[], actionId: string): Binding[] {
  const owned = bindings.filter((b) => b.actionId === actionId);
  const override = owned.find((b) => b.id === `${actionId}#override`);
  if (override !== undefined) return [override];
  const nonTwin = owned.filter((b) => !b.id.endsWith("#mod-twin"));
  return nonTwin.length > 0 ? nonTwin : owned;
}

/** The single-binding form of displayBindingsFor (the Settings section shows
 * one chord per row): the override when present, else the platform base
 * entry, else whatever binding the action has left. */
export function displayBindingFor(bindings: readonly Binding[], actionId: string): Binding | undefined {
  return displayBindingsFor(bindings, actionId)[0];
}

/** Whether the action's effective bindings differ from its default map
 * entries - including the unbound (override `chord: null`) case, where the
 * effective set is empty. The comparison is pref-aware about the ONE
 * conditional default entry: the "?" character-key trigger is part of the
 * default map only while characterKeyTriggers is on, so turning the pref off
 * (which unregisters that binding) is not itself a customization. */
export function isActionCustomized(
  bindings: readonly Binding[],
  actionId: string,
  characterKeyTriggers: boolean,
): boolean {
  const effective = bindings
    .filter((b) => b.actionId === actionId)
    .map((b) => `${b.scope} ${serializeChord(b.chord)}`)
    .sort();
  const defaults = defaultBindingChordsForAction(actionId)
    .filter((info) => characterKeyTriggers || info.id !== CHARACTER_KEY_TRIGGER_BINDING_ID)
    .map((info) => `${info.scope} ${info.serialized}`)
    .sort();
  return effective.length !== defaults.length || effective.some((entry, i) => entry !== defaults[i]);
}
