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
// Extra-modifier semantics are equally legacy-faithful (tinykeys matches
// strictly, so each chord lists as OPTIONAL exactly the modifiers the legacy
// listener ignored):
//
//   - ⌘K / ⌘I / ⌘J: the legacy AppShell listener checked only
//     metaKey||ctrlKey + key - NO shift/alt guard - so ⌘⇧K, ⌘⌥I,
//     Ctrl+Shift+J etc. all fired, on either OR BOTH of Meta/Ctrl. Chords
//     are $mod+[Shift]+[Alt]+… plus legacyEitherMod (the other of
//     Meta/Ctrl also optional on the entry and its twin). RATIONALE: this
//     permissiveness means these chords keep hijacking the browser's
//     DevTools shortcuts (⌘⌥I, Ctrl+Shift+J) exactly like the legacy
//     listeners did - deliberately revisiting THAT is Phase 3 policy, not
//     this PR.
//   - ⌘': extra Shift allowed ([Shift] - the legacy listener had no shift
//     guard), extra Alt NOT (the legacy !event.altKey AltGr guard).
//   - ⌘B: strict - the legacy listener guarded !event.altKey &&
//     !event.shiftKey.
//   - Escape (settings.close): every modifier optional - the legacy
//     listener checked only event.key === "Escape".
//
// settings.close is scope-gated: it lives in the settings scope, NOT global.
// The Settings pane pushes that scope while it is open.

import { ACTIONS } from "./actions";
import { type KeySequence, parseChord, serializeChord, withOptionalModifier } from "./chord";
import { type Binding, type BindingInput, GLOBAL_SCOPE, type KeybindingsRegistry } from "./registry";

export const SETTINGS_SCOPE = "settings";

/** A default-map entry: a BindingInput plus the module-internal
 * legacyEitherMod marker (stripped before registerBinding - see modPair). */
interface DefaultBindingInput extends BindingInput {
  /** The action's user-facing title, shown by the Settings keybindings
   * section. Kept on the default-map entry itself so the section's action
   * list is sourced live from this map - a new action appears there
   * automatically, and no second hand-maintained list can go stale (the
   * survey's stale-HELP_ROWS lesson). */
  title: string;
  /** Legacy "either or both of Meta/Ctrl" permissiveness: the AppShell
   * ⌘K/⌘I/⌘J, RailHost ⌘B and SelectionQuote ⌘' listeners all checked
   * metaKey||ctrlKey with no regard for the OTHER modifier's state (their
   * guards rejected only Alt/Shift), so Meta+Ctrl+<key> fired. The
   * complement of $mod is added as an OPTIONAL modifier on both this entry
   * and its twin. Alt/Shift strictness is unaffected. */
  legacyEitherMod?: boolean;
}

export const DEFAULT_BINDINGS: readonly DefaultBindingInput[] = [
  {
    id: ACTIONS.paletteOpen,
    actionId: ACTIONS.paletteOpen,
    title: "Open the command palette",
    chord: "$mod+[Shift]+[Alt]+K",
    allowInEditable: true,
    allowInModal: true,
    legacyEitherMod: true,
  },
  // ignoreIfDefaultPrevented: false - RailHost's ⌘B listener guards only the
  // editable target; it has no defaultPrevented check and toggles the rail
  // even when an inner handler already claimed the keydown.
  {
    id: ACTIONS.railToggle,
    actionId: ACTIONS.railToggle,
    title: "Toggle the sidebar",
    chord: "$mod+B",
    allowInEditable: false,
    allowInModal: true,
    ignoreIfDefaultPrevented: false,
    legacyEitherMod: true,
  },
  {
    id: ACTIONS.composerFocus,
    actionId: ACTIONS.composerFocus,
    title: "Focus the composer",
    chord: "$mod+[Shift]+[Alt]+I",
    allowInEditable: true,
    legacyEitherMod: true,
  },
  {
    id: ACTIONS.nextNeedsYou,
    actionId: ACTIONS.nextNeedsYou,
    title: "Go to the next session needing you",
    chord: "$mod+[Shift]+[Alt]+J",
    allowInEditable: true,
    legacyEitherMod: true,
  },
  // The dispatcher preventDefaults when a handler accepts this chord; the
  // legacy ⌘' listener never preventDefault'd. That superset is deliberate
  // and harmless: nothing meaningful defaults on Ctrl/⌘+'.
  {
    id: ACTIONS.selectionQuote,
    actionId: ACTIONS.selectionQuote,
    title: "Quote the selection into the composer",
    chord: "$mod+[Shift]+'",
    allowInEditable: true,
    allowInModal: true,
    legacyEitherMod: true,
  },
  {
    id: ACTIONS.settingsClose,
    actionId: ACTIONS.settingsClose,
    title: "Close settings",
    chord: "[Control]+[Alt]+[Shift]+[Meta]+Escape",
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
//
// Returns the two entries to register for a $mod-sourced binding (platform
// base + twin), or null when the chord has no $mod. With legacyEitherMod the
// complement of $mod is added as an OPTIONAL modifier on both entries, so
// pressing Meta and Ctrl together also fires - legacy accepted either or
// both regardless of the other modifier's state.
function modPair(input: DefaultBindingInput): [BindingInput, BindingInput] | null {
  if (typeof input.chord !== "string" || !input.chord.includes("$mod")) return null;
  const resolved = serializeChord(parseChord(input.chord));
  let twinString: string | null = null;
  for (const mod of ["Meta", "Control"] as const) {
    const candidate = input.chord.replaceAll("$mod", mod);
    if (serializeChord(parseChord(candidate)) !== resolved) twinString = candidate;
  }
  if (twinString === null) return null;
  const { legacyEitherMod: _legacyEitherMod, title: _title, ...base } = input;
  const twin: BindingInput = { ...base, id: `${base.id}#mod-twin`, chord: twinString };
  if (!input.legacyEitherMod) return [base, twin];
  return [
    { ...base, chord: withComplementOptional(parseChord(input.chord as string)) },
    { ...twin, chord: withComplementOptional(parseChord(twinString)) },
  ];
}

// Adds the OTHER of Meta/Ctrl as an optional modifier to every press that
// requires one of them.
function withComplementOptional(sequence: KeySequence): KeySequence {
  let complement: string | null = null;
  for (const press of sequence) {
    if (press.modifiers.includes("Meta")) complement = "Control";
    else if (press.modifiers.includes("Control")) complement = "Meta";
  }
  if (complement === null) return sequence;
  return withOptionalModifier(sequence, complement);
}

/** Registers the full default map (plus each $mod chord's cross-platform
 * twin); returns the registered entries. */
export function registerDefaultBindings(registry: KeybindingsRegistry): Binding[] {
  const registered: Binding[] = [];
  for (const input of DEFAULT_BINDINGS) {
    const pair = modPair(input);
    for (const entry of pair ?? [input]) {
      registered.push(registry.getState().registerBinding(entry));
    }
  }
  return registered;
}

/** Registers only the given action's default entries (plus the $mod twin),
 * the per-action equivalent of registerDefaultBindings the overrides store
 * uses to restore one action's defaults. Throws on an unknown action id. */
export function registerDefaultBindingsForAction(registry: KeybindingsRegistry, actionId: string): Binding[] {
  const inputs = DEFAULT_BINDINGS.filter((input) => input.actionId === actionId);
  if (inputs.length === 0) throw new Error(`unknown keybinding action "${actionId}"`);
  const registered: Binding[] = [];
  for (const input of inputs) {
    const pair = modPair(input);
    for (const entry of pair ?? [input]) {
      registered.push(registry.getState().registerBinding(entry));
    }
  }
  return registered;
}

export interface DefaultChordInfo {
  scope: string;
  serialized: string;
}

/** The (scope, serialized chord) pairs registerDefaultBindingsForAction would
 * register for the action, WITHOUT registering them: the validation layer
 * simulates dropped-override restorations against these. Throws on an
 * unknown action id. */
export function defaultBindingChordsForAction(actionId: string): DefaultChordInfo[] {
  const inputs = DEFAULT_BINDINGS.filter((input) => input.actionId === actionId);
  if (inputs.length === 0) throw new Error(`unknown keybinding action "${actionId}"`);
  const chords: DefaultChordInfo[] = [];
  for (const input of inputs) {
    const pair = modPair(input);
    for (const entry of pair ?? [input]) {
      chords.push({
        scope: entry.scope ?? GLOBAL_SCOPE,
        serialized: serializeChord(typeof entry.chord === "string" ? parseChord(entry.chord) : entry.chord),
      });
    }
  }
  return chords;
}
