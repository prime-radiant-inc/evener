// Settings -> Keybindings: a READ-ONLY listing of the effective bindings,
// plus the character-key-triggers toggle. The action list and the per-action
// display binding are sourced from keybindings/display.ts - the same module
// the cheatsheet overlay reads - never a hand-maintained copy (the survey's
// stale-HELP_ROWS lesson). The chords come from the live registry, so hub
// overrides applied by the keybindings store show up the moment they
// reconcile. Editing affordances are deliberately absent; the shortcuts
// editor is a later phase (4b).

import { useStore } from "zustand";
import { chordDisplayKeys, serializeChord } from "../../../keybindings/chord";
import { ACTION_DISPLAY_ROWS, displayBindingFor, isActionCustomized } from "../../../keybindings/display";
import { keybindingsRegistry } from "../../../keybindings/registry";
import { useKeybindingsStore } from "../../../stores/keybindings";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { KeyHint, Switch } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./keybindings.module.css";

const CLASS = {
  root: requireClass(styles.root, "keybindings.module.css", "root"),
  intro: requireClass(styles.intro, "keybindings.module.css", "intro"),
  toggle: requireClass(styles.toggle, "keybindings.module.css", "toggle"),
  help: requireClass(styles.help, "keybindings.module.css", "help"),
  status: requireClass(styles.status, "keybindings.module.css", "status"),
  warningList: requireClass(styles.warningList, "keybindings.module.css", "warningList"),
  error: requireClass(styles.error, "keybindings.module.css", "error"),
  list: requireClass(styles.list, "keybindings.module.css", "list"),
  row: requireClass(styles.row, "keybindings.module.css", "row"),
  label: requireClass(styles.label, "keybindings.module.css", "label"),
  marker: requireClass(styles.marker, "keybindings.module.css", "marker"),
  unbound: requireClass(styles.unbound, "keybindings.module.css", "unbound"),
  chord: requireClass(styles.chord, "keybindings.module.css", "chord"),
};

export function KeybindingsSection() {
  const hubSupport = useKeybindingsStore((s) => s.hubSupport);
  const hubError = useKeybindingsStore((s) => s.hubError);
  const warnings = useKeybindingsStore((s) => s.warnings);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);
  const characterKeyTriggers = usePrefsStore((s) => s.characterKeyTriggers);

  let status: string | undefined;
  if (hubSupport === "unknown") {
    status = "Waiting for the hub connection to report keybindings support.";
  } else if (hubSupport === "unsupported") {
    status = "This hub does not support synced keybinding overrides. The built-in defaults are in effect.";
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>
        The keyboard shortcuts currently in effect. Bindings marked Customized come from this hub's synced overrides.
      </p>

      {/* The WCAG 2.1.4 character-key turn-off (the p4 plan's Design
          decision 3): off unregisters the "?" cheatsheet trigger, leaving
          every shortcut on a modifier chord. Browser-local (prefs store),
          by controller ruling. */}
      <div className={CLASS.toggle}>
        <Switch
          label="Character-key shortcuts"
          checked={characterKeyTriggers}
          onChange={(value) => prefsStore.getState().setCharacterKeyTriggers(value)}
        />
        <p className={CLASS.help}>
          When on, pressing ? outside a text field opens the keyboard shortcuts overlay. Turn off to keep every shortcut
          on a modifier chord.
        </p>
      </div>

      {status !== undefined && (
        <p className={CLASS.status} role="status">
          {status}
        </p>
      )}
      {hubError !== null && (
        <p className={CLASS.error} role="alert">
          Could not load keybinding overrides: {hubError}
        </p>
      )}
      {warnings.length > 0 && (
        <div className={CLASS.status} role="status">
          <p>Some saved overrides were skipped:</p>
          <ul className={CLASS.warningList}>
            {warnings.map((warning) => (
              <li key={warning.message}>{warning.message}</li>
            ))}
          </ul>
        </div>
      )}

      <ul className={CLASS.list}>
        {ACTION_DISPLAY_ROWS.map((row) => {
          const binding = displayBindingFor(bindings, row.actionId);
          return (
            <li className={CLASS.row} key={row.actionId}>
              <span className={CLASS.label}>
                {row.title}
                {isActionCustomized(bindings, row.actionId, characterKeyTriggers) && (
                  <span className={CLASS.marker}>Customized</span>
                )}
              </span>
              {binding === undefined ? (
                <span className={CLASS.unbound}>Unbound</span>
              ) : (
                <span className={CLASS.chord}>
                  {binding.chord.map((press) => (
                    <KeyHint key={serializeChord([press])} keys={chordDisplayKeys(press)} />
                  ))}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
