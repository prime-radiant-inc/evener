// The default binding map: exactly the six shell chords that today live in six
// independent keydown listeners (AppShell, RailHost, SelectionQuote,
// Settings). Per-binding policy per chord matches today's behavior:
//
//   - ⌘K / ⌘I / ⌘J / ⌘' fire from editable targets (allowInEditable: true):
//     none of today's listeners for these chords guards on the target.
//   - ⌘B suppresses in editable targets (allowInEditable: false):
//     RailHost guards so Ctrl+B keeps its emacs "cursor back" meaning in
//     native text fields.
//   - settings.close fires from editable targets too: today's Settings Escape
//     listener is document-level with no editable guard.
//   - ⌘K, ⌘B and ⌘' fire while a modal is open (allowInModal: true):
//     AppShell deliberately exempts ⌘K from its blockedByOpenModal guard
//     ("opening the palette while it's already open is a harmless no-op
//     reset"), and RailHost's ⌘B / SelectionQuote's ⌘' listeners have no
//     modal check at all. ⌘I / ⌘J keep the default suppression (AppShell's
//     blockedByOpenModal covered exactly them), and so does settings.close -
//     today a modal dialog claims Escape first via its FocusScope/OverlayPanel
//     handler, which the dispatcher's per-binding defaultPrevented gate
//     reproduces.
//
// settings.close is scope-gated: it lives in the settings scope, NOT global.
// The Settings pane pushes that scope while it is open.

import { ACTIONS } from "./actions";
import { parseChord, serializeChord } from "./chord";
import type { Binding, BindingInput, KeybindingsRegistry } from "./registry";

export const SETTINGS_SCOPE = "settings";

export const DEFAULT_BINDINGS: readonly BindingInput[] = [
  {
    id: ACTIONS.paletteOpen,
    actionId: ACTIONS.paletteOpen,
    chord: "$mod+K",
    allowInEditable: true,
    allowInModal: true,
  },
  // ignoreIfDefaultPrevented: false - RailHost's ⌘B listener guards only the
  // editable target; it has no defaultPrevented check and toggles the rail
  // even when an inner handler already claimed the keydown.
  {
    id: ACTIONS.railToggle,
    actionId: ACTIONS.railToggle,
    chord: "$mod+B",
    allowInEditable: false,
    allowInModal: true,
    ignoreIfDefaultPrevented: false,
  },
  { id: ACTIONS.composerFocus, actionId: ACTIONS.composerFocus, chord: "$mod+I", allowInEditable: true },
  { id: ACTIONS.nextNeedsYou, actionId: ACTIONS.nextNeedsYou, chord: "$mod+J", allowInEditable: true },
  {
    id: ACTIONS.selectionQuote,
    actionId: ACTIONS.selectionQuote,
    chord: "$mod+'",
    allowInEditable: true,
    allowInModal: true,
  },
  {
    id: ACTIONS.settingsClose,
    actionId: ACTIONS.settingsClose,
    chord: "Escape",
    scope: SETTINGS_SCOPE,
    allowInEditable: true,
  },
];

// tinykeys resolves "$mod" to Meta on Apple platforms and Control everywhere
// else AT PARSE TIME, but every pre-dispatcher listener these bindings replace
// accepted `event.metaKey || event.ctrlKey` on EVERY platform (a Mac user's
// Ctrl+K opened the palette exactly like ⌘K). Registering the cross-platform
// twin of each $mod chord preserves that. It also keeps the shell's jsdom
// suites - where $mod resolves to Control but userEvent/fireEvent press Meta
// chords - exercising the same code path production does.
function modTwin(input: BindingInput): BindingInput | null {
  if (typeof input.chord !== "string" || !input.chord.includes("$mod")) return null;
  const resolved = serializeChord(parseChord(input.chord));
  for (const mod of ["Meta", "Control"] as const) {
    const candidate = input.chord.replaceAll("$mod", mod);
    if (serializeChord(parseChord(candidate)) !== resolved) {
      return { ...input, id: `${input.id}#mod-twin`, chord: candidate };
    }
  }
  return null;
}

/** Registers the full default map (plus each $mod chord's cross-platform
 * twin); returns the registered entries. */
export function registerDefaultBindings(registry: KeybindingsRegistry): Binding[] {
  const registered: Binding[] = [];
  for (const input of DEFAULT_BINDINGS) {
    registered.push(registry.getState().registerBinding(input));
    const twin = modTwin(input);
    if (twin !== null) registered.push(registry.getState().registerBinding(twin));
  }
  return registered;
}
