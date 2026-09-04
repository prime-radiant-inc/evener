// Settings -> Keybindings: the capture-based binding editor (Phase 4b).
// Clicking a row's chord enters capture mode (HotkeyRecorder-style: the
// prompt shows held modifiers live, the first non-modifier press records the
// chord, Enter saves, Escape cancels); per-action Unbind and Reset affordances
// sit beside it. Writes go through the keybindings store's patchOverrides,
// whose pre-flight validation rejects a conflicting (or reserved, or
// unparseable) chord BEFORE the hub write - that message renders inline on
// the row. The action list and per-action display bindings are sourced from
// keybindings/display.ts - the same module the cheatsheet overlay reads -
// never a hand-maintained copy (the survey's stale-HELP_ROWS lesson).
//
// Editing requires a hub with synced-override support (hubSupport ===
// "supported"): the overrides layer is hub-only by design, so unknown and
// unsupported hubs get the read-only listing with the existing status text.
//
// The overrides model owns an action's WHOLE chord set, so the editor edits
// each action's base chord only; cheatsheet.toggle's conditional "?" entry
// (managed by the character-key setting, per shell/cheatsheet/
// cheatsheetController.ts) renders read-only with a note.

import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useRef, useState } from "react";
import { useStore } from "zustand";
import { type Chord, chordDisplayKeys, modifierDisplayKey, serializeChord } from "../../../keybindings/chord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID } from "../../../keybindings/defaults";
import {
  ACTION_DISPLAY_ROWS,
  displayBindingFor,
  displayBindingsFor,
  isActionCustomized,
} from "../../../keybindings/display";
import { type Binding, keybindingsRegistry } from "../../../keybindings/registry";
import type { OverrideRule } from "../../../keybindings/validation";
import { keybindingsStore, useKeybindingsStore } from "../../../stores/keybindings";
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
  chordButton: requireClass(styles.chordButton, "keybindings.module.css", "chordButton"),
  controls: requireClass(styles.controls, "keybindings.module.css", "controls"),
  rowError: requireClass(styles.rowError, "keybindings.module.css", "rowError"),
  note: requireClass(styles.note, "keybindings.module.css", "note"),
  actionButton: requireClass(styles.actionButton, "keybindings.module.css", "actionButton"),
  captureWrap: requireClass(styles.captureWrap, "keybindings.module.css", "captureWrap"),
  capture: requireClass(styles.capture, "keybindings.module.css", "capture"),
  capturePrompt: requireClass(styles.capturePrompt, "keybindings.module.css", "capturePrompt"),
  captureHint: requireClass(styles.captureHint, "keybindings.module.css", "captureHint"),
  captureError: requireClass(styles.captureError, "keybindings.module.css", "captureError"),
};

// The keys that are modifiers themselves: holding one updates the live
// preview but never records a chord.
const MODIFIER_KEYS = new Set(["Control", "Alt", "Shift", "Meta"]);

/** The held modifiers of a key event, in the chord module's canonical order
 * (chord.ts's MODIFIER_ORDER), so a recorded chord serializes canonically. */
function eventModifiers(event: ReactKeyboardEvent): string[] {
  const modifiers: string[] = [];
  if (event.ctrlKey) modifiers.push("Control");
  if (event.altKey) modifiers.push("Alt");
  if (event.shiftKey) modifiers.push("Shift");
  if (event.metaKey) modifiers.push("Meta");
  return modifiers;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** The full replacement rule set a single-action edit produces: the store's
 * patch semantics are whole-payload (an action the payload drops gets its
 * defaults restored), so every edit re-sends the current overrides with this
 * action's rule swapped - or REMOVED (chord undefined) for reset-to-default.
 * Composition is from the hub's RAW rules (rawOverrides), NOT the validated
 * `overrides`: a rule validation skipped (an unknown action from a newer
 * client, an unparseable chord) is still the hub's state and passes through
 * untouched - composing from the validated set would silently delete it. */
function replacementRules(actionId: string, chord: string | null | undefined): OverrideRule[] {
  const current = keybindingsStore.getState().rawOverrides;
  const rest = current.filter((rule) => rule.action !== actionId);
  if (chord === undefined) return [...rest];
  return [...rest, { action: actionId, chord }];
}

interface CaptureBoxProps {
  title: string;
  /** Returns null on success (the box unmounts) or the message to show. */
  onSave: (chord: Chord) => Promise<string | null>;
  /** `refocus` is true when the capture ended from the keyboard (Escape) -
   * the row returns focus to its chord button - and false when it ended by
   * clicking away (focus already went where the user put it). */
  onCancel: (refocus: boolean) => void;
}

/** The capture widget: a focusable box that consumes EVERY keydown while it
 * is alive. preventDefault + stopPropagation keep each press from the
 * window-level dispatcher - a captured chord must never fire the action it
 * would replace, and rail.toggle's binding opts out of the
 * defaultPrevented gate, so stopping propagation is the load-bearing half -
 * and from the settings scope's own Escape (settings.close's
 * ignoreIfDefaultPrevented gate is the backstop there, so capture's Escape
 * cancels the capture instead of closing the pane).
 *
 * Single-press chords only: the default map is single-press throughout and
 * multi-press overlap checking is deliberately coarser (chord.ts's
 * chordsOverlap), so the editor does not author sequences. Plain Enter saves
 * and plain Escape cancels; either key WITH a modifier records as a chord. */
function CaptureBox({ title, onSave, onCancel }: CaptureBoxProps) {
  const [held, setHeld] = useState<string[]>([]);
  const [captured, setCaptured] = useState<Chord | null>(null);
  const [error, setError] = useState<string | null>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  // A save is a hub round trip; keypresses that land while it is in flight
  // are ignored rather than queued into a second patch.
  const savingRef = useRef(false);

  useEffect(() => {
    boxRef.current?.focus();
  }, []);

  // Click-away cancels even when the click target is not focusable: onBlur
  // alone only fires when focus MOVES (to another control), so clicking
  // non-focusable text or empty space would leave the capture live. A
  // document-level pointerdown in the capture phase sees every press,
  // wherever it lands; a press outside the box cancels with refocus:false
  // (focus goes where the user put it - or stays, for a non-focusable
  // target). The listener dies with the box (unmount on cancel/save).
  useEffect(() => {
    function onPointerDown(event: PointerEvent): void {
      const box = boxRef.current;
      if (box !== null && event.target instanceof Node && !box.contains(event.target)) {
        onCancel(false);
      }
    }
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => document.removeEventListener("pointerdown", onPointerDown, true);
  }, [onCancel]);

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): void {
    event.preventDefault();
    event.stopPropagation();
    if (savingRef.current) return;
    const modifiers = eventModifiers(event);
    if (MODIFIER_KEYS.has(event.key)) {
      setHeld(modifiers);
      return;
    }
    if (event.key === "Escape" && modifiers.length === 0) {
      onCancel(true);
      return;
    }
    if (event.key === "Enter" && modifiers.length === 0) {
      if (captured === null) return;
      savingRef.current = true;
      const chord = captured;
      void onSave(chord).then((saveError) => {
        savingRef.current = false;
        // On success the parent unmounted this box; only a failure needs
        // the state write.
        if (saveError !== null) setError(saveError);
      });
      return;
    }
    setError(null);
    // ASCII letters normalize to uppercase: KeyboardEvent.key for a
    // mod chord arrives lowercase ("p" for Ctrl+P) while the default map
    // writes letters uppercase, and tinykeys matches case-insensitively, so
    // normalizing keeps display and serialization consistent with the
    // existing rows without changing what matches. The test is ASCII-only on
    // purpose: String.toUpperCase can CHANGE LENGTH on non-ASCII input
    // ("ß".toUpperCase() === "SS"), which would corrupt the chord.
    setCaptured({
      modifiers,
      optionalModifiers: [],
      key: /^[a-z]$/.test(event.key) ? event.key.toUpperCase() : event.key,
    });
  }

  function handleKeyUp(event: ReactKeyboardEvent<HTMLDivElement>): void {
    if (MODIFIER_KEYS.has(event.key)) setHeld(eventModifiers(event));
  }

  return (
    <div className={CLASS.captureWrap}>
      {/* Clicking away cancels: the box is modal-ish, and focus leaving it
          (another row's chord button, the toggle) ends the capture via
          onBlur; the pointerdown listener above covers clicks that never
          move focus (non-focusable text, empty space). The
          textbox role names what it is - a keyboard-capture surface in the
          spirit of HotkeyRecorder's input; aria-readonly because every key
          is intercepted rather than typed. */}
      {/* biome-ignore lint/a11y/useSemanticElements: a real <input> cannot hold the KeyHint kbd rendering or the save/cancel hint - only its role is wanted */}
      <div
        ref={boxRef}
        className={CLASS.capture}
        role="textbox"
        aria-readonly="true"
        tabIndex={-1}
        aria-label={`Press the new shortcut for ${title}. Enter saves, Escape cancels.`}
        onKeyDown={handleKeyDown}
        onKeyUp={handleKeyUp}
        onBlur={() => onCancel(false)}
      >
        {captured !== null ? (
          <KeyHint keys={chordDisplayKeys(captured)} />
        ) : held.length > 0 ? (
          <KeyHint keys={held.map(modifierDisplayKey)} />
        ) : (
          <span className={CLASS.capturePrompt}>Press new shortcut…</span>
        )}
        <span className={CLASS.captureHint}>Enter to save · Esc to cancel</span>
      </div>
      {error !== null && (
        <p className={CLASS.captureError} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

interface KeybindingRowProps {
  actionId: string;
  title: string;
  /** False on hubs without synced-override support: the row renders read-only. */
  editable: boolean;
  bindings: readonly Binding[];
  characterKeyTriggers: boolean;
}

function KeybindingRow({ actionId, title, editable, bindings, characterKeyTriggers }: KeybindingRowProps) {
  const [capturing, setCapturing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const hasOverride = useKeybindingsStore((s) => s.overrides.some((rule) => rule.action === actionId));
  const chordButtonRef = useRef<HTMLButtonElement>(null);
  // Set when a capture ends from the keyboard (Escape cancel, Enter save):
  // focus returns to the chord button so the keyboard flow continues where
  // it started. A click-away cancel does NOT set it - focus already went
  // where the user put it.
  const refocusOnCloseRef = useRef(false);

  useEffect(() => {
    if (!capturing && refocusOnCloseRef.current) {
      refocusOnCloseRef.current = false;
      chordButtonRef.current?.focus();
    }
  }, [capturing]);

  const binding = displayBindingFor(bindings, actionId);
  const customized = isActionCustomized(bindings, actionId, characterKeyTriggers);
  // Extra default entries beyond the platform base entry - in practice
  // exactly cheatsheet.toggle's conditional "?" trigger. The overrides model
  // owns an action's whole chord set, so these are the setting's to manage,
  // not the editor's: read-only, with the consequence spelled out.
  const conditionalEntries = displayBindingsFor(bindings, actionId).filter(
    (entry) => entry.id === CHARACTER_KEY_TRIGGER_BINDING_ID,
  );

  async function saveCapture(chord: Chord): Promise<string | null> {
    try {
      await keybindingsStore.getState().patchOverrides(replacementRules(actionId, serializeChord([chord])));
    } catch (saveError) {
      return errorMessage(saveError);
    }
    refocusOnCloseRef.current = true;
    setCapturing(false);
    setError(null);
    return null;
  }

  function cancelCapture(refocus: boolean): void {
    refocusOnCloseRef.current = refocus;
    setCapturing(false);
  }

  async function applyRules(rules: OverrideRule[]): Promise<void> {
    try {
      await keybindingsStore.getState().patchOverrides(rules);
      setError(null);
    } catch (patchError) {
      setError(errorMessage(patchError));
    }
  }

  const chordCell =
    binding === undefined ? (
      <span className={CLASS.unbound}>Unbound</span>
    ) : (
      <span className={CLASS.chord}>
        {binding.chord.map((press) => (
          <KeyHint key={serializeChord([press])} keys={chordDisplayKeys(press)} />
        ))}
      </span>
    );

  return (
    <li className={CLASS.row}>
      <span className={CLASS.label}>
        {title}
        {customized && <span className={CLASS.marker}>Customized</span>}
      </span>
      {capturing ? (
        <CaptureBox title={title} onSave={saveCapture} onCancel={cancelCapture} />
      ) : editable ? (
        <span className={CLASS.controls}>
          <button
            ref={chordButtonRef}
            type="button"
            className={CLASS.chordButton}
            aria-label={binding === undefined ? `Set a shortcut for ${title}` : `Change the shortcut for ${title}`}
            onClick={() => {
              setError(null);
              setCapturing(true);
            }}
          >
            {binding === undefined ? <span className={CLASS.unbound}>Unbound</span> : chordCell}
          </button>
          {binding !== undefined && (
            <button
              type="button"
              className={CLASS.actionButton}
              onClick={() => void applyRules(replacementRules(actionId, null))}
            >
              Unbind
            </button>
          )}
          {hasOverride && (
            <button
              type="button"
              className={CLASS.actionButton}
              onClick={() => void applyRules(replacementRules(actionId, undefined))}
            >
              Reset
            </button>
          )}
        </span>
      ) : (
        chordCell
      )}
      {conditionalEntries.length > 0 && (
        <p className={CLASS.note}>
          Also opens on{" "}
          {conditionalEntries.map((entry) =>
            entry.chord.map((press) => <KeyHint key={serializeChord([press])} keys={chordDisplayKeys(press)} />),
          )}{" "}
          while the character-key setting above is on. That entry follows the setting, not this editor; changing or
          unbinding this shortcut replaces it too.
        </p>
      )}
      {error !== null && (
        <p className={CLASS.rowError} role="alert">
          {error}
        </p>
      )}
    </li>
  );
}

export function KeybindingsSection() {
  const hubSupport = useKeybindingsStore((s) => s.hubSupport);
  const hubError = useKeybindingsStore((s) => s.hubError);
  const warnings = useKeybindingsStore((s) => s.warnings);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);
  const characterKeyTriggers = usePrefsStore((s) => s.characterKeyTriggers);
  const editable = hubSupport === "supported";

  let status: string | undefined;
  if (hubSupport === "unknown") {
    status = "Waiting for the hub connection to report keybindings support.";
  } else if (hubSupport === "unsupported") {
    status = "This hub does not support synced keybinding overrides. The built-in defaults are in effect.";
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>
        {editable
          ? "Click a shortcut to change it; Enter saves and Escape cancels. Changes sync to this hub. Bindings marked Customized come from this hub's synced overrides."
          : "The keyboard shortcuts currently in effect. Bindings marked Customized come from this hub's synced overrides."}
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
        {ACTION_DISPLAY_ROWS.map((row) => (
          <KeybindingRow
            key={row.actionId}
            actionId={row.actionId}
            title={row.title}
            editable={editable}
            bindings={bindings}
            characterKeyTriggers={characterKeyTriggers}
          />
        ))}
      </ul>
    </div>
  );
}
