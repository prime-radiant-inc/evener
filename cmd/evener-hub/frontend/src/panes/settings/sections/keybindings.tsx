// Settings -> Keybindings: a READ-ONLY listing of the effective bindings.
// The action list is sourced live from the keybindings module's default map
// (DEFAULT_BINDINGS is also the validator's action universe), never a
// hand-maintained copy - the survey's stale-HELP_ROWS lesson. The chords come
// from the live registry, so hub overrides applied by the keybindings store
// show up the moment they reconcile. Editing affordances are deliberately
// absent; the shortcuts editor is a later phase.

import { useStore } from "zustand";
import { chordDisplayKeys, serializeChord } from "../../../keybindings/chord";
import { DEFAULT_BINDINGS, defaultBindingChordsForAction } from "../../../keybindings/defaults";
import { type Binding, keybindingsRegistry } from "../../../keybindings/registry";
import { useKeybindingsStore } from "../../../stores/keybindings";
import { KeyHint } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./keybindings.module.css";

const CLASS = {
  root: requireClass(styles.root, "keybindings.module.css", "root"),
  intro: requireClass(styles.intro, "keybindings.module.css", "intro"),
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

interface ActionRow {
  actionId: string;
  title: string;
}

// One row per action, in default-map order. Module-level because it derives
// entirely from the compiled-in default map; the effective chord each row
// shows is read from the live registry at render.
const ACTION_ROWS: readonly ActionRow[] = (() => {
  const seen = new Set<string>();
  const rows: ActionRow[] = [];
  for (const input of DEFAULT_BINDINGS) {
    if (seen.has(input.actionId)) continue;
    seen.add(input.actionId);
    rows.push({ actionId: input.actionId, title: input.title });
  }
  return rows;
})();

/** The binding to display for an action: the override when present, else the
 * platform base entry (its `#mod-twin` shows the same chord on this
 * platform), else whatever binding the action has left. */
function displayBindingFor(bindings: readonly Binding[], actionId: string): Binding | undefined {
  const owned = bindings.filter((b) => b.actionId === actionId);
  return owned.find((b) => b.id === `${actionId}#override`) ?? owned.find((b) => b.id === actionId) ?? owned[0];
}

/** Whether the action's effective bindings differ from its default map
 * entries - including the unbound (override `chord: null`) case, where the
 * effective set is empty. */
function isCustomized(bindings: readonly Binding[], actionId: string): boolean {
  const effective = bindings
    .filter((b) => b.actionId === actionId)
    .map((b) => `${b.scope} ${serializeChord(b.chord)}`)
    .sort();
  const defaults = defaultBindingChordsForAction(actionId)
    .map((d) => `${d.scope} ${d.serialized}`)
    .sort();
  return effective.length !== defaults.length || effective.some((entry, i) => entry !== defaults[i]);
}

export function KeybindingsSection() {
  const hubSupport = useKeybindingsStore((s) => s.hubSupport);
  const hubError = useKeybindingsStore((s) => s.hubError);
  const warnings = useKeybindingsStore((s) => s.warnings);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);

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
        {ACTION_ROWS.map((row) => {
          const binding = displayBindingFor(bindings, row.actionId);
          return (
            <li className={CLASS.row} key={row.actionId}>
              <span className={CLASS.label}>
                {row.title}
                {isCustomized(bindings, row.actionId) && <span className={CLASS.marker}>Customized</span>}
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
