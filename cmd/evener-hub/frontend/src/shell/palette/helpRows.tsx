import { ACTIONS, type Binding, chordDisplayKeys, displayBindingsFor, serializeChord } from "@evener/appwire-client";
import { Fragment, useEffect } from "react";
import { useStore } from "zustand";
import { keybindingsRegistry } from "../../keybindings/appRegistry";
import { KeyHint, type ShortcutListItem } from "../../widgets";
import { installKeybindings } from "../installKeybindings";

type HelpRow = { keys: string[]; label: string } | { actionId: string; label: string };

// The app-wide shortcut legend, rendered through the shared ShortcutList
// widget (Mod resolves to the reviewing platform's own ⌘/Ctrl).
//
// Rows naming an action take their chords from the keybinding registry, so a
// rebound chord shows truthfully and an unbound action drops out, exactly as
// the cheatsheet overlay does. The remaining rows are chords owned by a
// component's own key handling (the palette's results list, the composer, the
// ask dock), which the registry does not know. The same chord legitimately
// appears twice (Mod+Enter: composer submit vs. ask dock/sandbox answer) -
// context-dependent, not a duplicate, so both rows stay with their own label.
const HELP_ROWS: readonly HelpRow[] = [
  { actionId: ACTIONS.paletteOpen, label: "open the command palette" },
  {
    keys: ["/"],
    label:
      "in the palette — filters app-global commands; a session command (goal, model, …) hands off to the composer instead",
  },
  { keys: ["/"], label: "at the start of an empty composer — opens the inline slash-command menu" },
  { keys: ["↑"], label: "move up the results list" },
  { keys: ["↓"], label: "move down the results list" },
  { keys: ["Enter"], label: "run the highlighted command (or open a search result)" },
  { keys: ["Mod", "Enter"], label: "open a search result in a new tab" },
  { keys: ["Shift", "Enter"], label: "jump to a turn in the current session" },
  { keys: ["Esc"], label: "close the palette (or back out of args mode)" },
  { actionId: ACTIONS.railToggle, label: "toggle the sidebar — inert while typing in an editable field" },
  { actionId: ACTIONS.composerFocus, label: "focus the composer" },
  { actionId: ACTIONS.nextNeedsYou, label: "go to the next session needing you" },
  { actionId: ACTIONS.selectionQuote, label: "quote the selection into the composer" },
  // Composer.tsx's own handleKeyDown: a plain Enter (or Mod+Enter, always)
  // submits - send now, or queue when the agent is mid-turn - and Shift+
  // Enter only steers when Enter-to-send is OFF (with it on, Shift+Enter is
  // a literal newline instead).
  { keys: ["Enter"], label: "send now (or queue, mid-turn) — when Enter-to-send is on" },
  { keys: ["Shift", "Enter"], label: "steer mid-turn — when Enter-to-send is off" },
  { keys: ["Mod", "Enter"], label: "send or queue now, regardless of the Enter-to-send setting" },
  // AskDock.tsx's handleBatchKeyDown / sandboxEscalation.tsx's own Mod+Enter.
  { keys: ["Mod", "Enter"], label: "answer or approve (ask dock questions, sandbox approvals)" },
  { keys: ["←"], label: "previous ask-dock question" },
  { keys: ["→"], label: "next ask-dock question" },
];

// A ShortcutList term is a single chord, so a multi-press override ("g g")
// shows its first press as the term and the later presses, each its own
// KeyHint, ahead of the label.
function helpRowsFor(bindings: readonly Binding[]): ShortcutListItem[] {
  return HELP_ROWS.flatMap((row): ShortcutListItem[] => {
    if ("keys" in row) return [row];
    return displayBindingsFor(bindings, row.actionId).flatMap((binding) => {
      const [first, ...rest] = binding.chord;
      if (first === undefined) return [];
      const label =
        rest.length === 0 ? (
          row.label
        ) : (
          <>
            {rest.map((press, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: repeated presses in a sequence ("g g g") are content-identical, so position is identity
              <Fragment key={`${i}:${serializeChord([press])}`}>
                <KeyHint keys={chordDisplayKeys(press)} />{" "}
              </Fragment>
            ))}
            {row.label}
          </>
        );
      return [{ keys: chordDisplayKeys(first), label }];
    });
  });
}

/** The legend rows, live against the registry. Installing the default
 * bindings here keeps the legend populated when the palette is mounted
 * without the shell, as every other chord-registering component does. */
export function useHelpRows(): ShortcutListItem[] {
  useEffect(() => installKeybindings(), []);
  const bindings = useStore(keybindingsRegistry, (state) => state.bindings);
  return helpRowsFor(bindings);
}
