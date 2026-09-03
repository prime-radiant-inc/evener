// The default binding map: exactly the six shell chords that today live in six
// independent keydown listeners (AppShell, RailHost, SelectionQuote,
// Settings). Editable-target policy per chord matches today's behavior:
//
//   - ⌘K / ⌘I / ⌘J / ⌘' fire from editable targets (allowInEditable: true):
//     none of today's listeners for these chords guards on the target.
//   - ⌘B suppresses in editable targets (allowInEditable: false):
//     RailHost guards so Ctrl+B keeps its emacs "cursor back" meaning in
//     native text fields.
//   - settings.close fires from editable targets too: today's Settings Escape
//     listener is document-level with no editable guard.
//
// settings.close is scope-gated: it lives in the settings scope, NOT global.
// Task 2 pushes that scope while the Settings pane is open.

import { ACTIONS } from "./actions";
import type { Binding, BindingInput, KeybindingsRegistry } from "./registry";

export const SETTINGS_SCOPE = "settings";

export const DEFAULT_BINDINGS: readonly BindingInput[] = [
  { id: ACTIONS.paletteOpen, actionId: ACTIONS.paletteOpen, chord: "$mod+K", allowInEditable: true },
  // ignoreIfDefaultPrevented: false - RailHost's ⌘B listener (RailHost.tsx:59-66)
  // guards only the editable target; it has no defaultPrevented check and
  // toggles the rail even when an inner handler already claimed the keydown.
  {
    id: ACTIONS.railToggle,
    actionId: ACTIONS.railToggle,
    chord: "$mod+B",
    allowInEditable: false,
    ignoreIfDefaultPrevented: false,
  },
  { id: ACTIONS.composerFocus, actionId: ACTIONS.composerFocus, chord: "$mod+I", allowInEditable: true },
  { id: ACTIONS.nextNeedsYou, actionId: ACTIONS.nextNeedsYou, chord: "$mod+J", allowInEditable: true },
  { id: ACTIONS.selectionQuote, actionId: ACTIONS.selectionQuote, chord: "$mod+'", allowInEditable: true },
  {
    id: ACTIONS.settingsClose,
    actionId: ACTIONS.settingsClose,
    chord: "Escape",
    scope: SETTINGS_SCOPE,
    allowInEditable: true,
  },
];

/** Registers the full default map; returns the registered entries. */
export function registerDefaultBindings(registry: KeybindingsRegistry): Binding[] {
  return DEFAULT_BINDINGS.map((input) => registry.getState().registerBinding(input));
}
