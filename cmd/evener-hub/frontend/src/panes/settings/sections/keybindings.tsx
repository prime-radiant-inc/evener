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
// Editing requires a hub with synced-override support whose override state
// is CURRENT: hubSupport === "supported" plus the store's `loaded` flag (set
// only by a confirmed apply in the current ready generation, cleared on
// rewire/disconnect), no in-flight refresh, and no hub error. Anything less
// gets the read-only listing with a truthful status line - a PATCH composed
// in the stale window would carry the previous hub's revision and payload.
//
// The overrides model owns an action's WHOLE chord set, so the editor edits
// each action's base chord only; cheatsheet.toggle's conditional "?" entry
// (managed by the character-key setting, per shell/cheatsheet/
// cheatsheetController.ts) renders read-only with a note.

import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useRef, useState } from "react";
import { useStore } from "zustand";
import { type Chord, chordDisplayKeys, modifierDisplayKey, serializeChord } from "../../../keybindings/chord";
import { CHARACTER_KEY_TRIGGER_BINDING_ID } from "../../../keybindings/defaults";
import { isIMECompositionKeydown } from "../../../keybindings/dispatcher";
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

// Browser key values that collide with the tinykeys GRAMMAR need the
// grammar's canonical, matchable spelling. The spacebar is the one case:
// its event.key is a literal " ", which is the grammar's press SEPARATOR -
// parseChord(" ") throws on the empty string and "Control+ " parses to an
// empty key, so a saved " " binding is dead on the next reconcile.
// tinykeys' matcher (matchKeybindingPress) accepts event.code as well as
// event.key, and the spacebar's code is "Space", so "Space" both survives
// the grammar and matches the key. Every other whitespace-adjacent key
// already arrives grammar-safe (Tab -> "Tab", Enter -> "Enter"). Kept at
// the capture seam on purpose: chord.ts's parser stays grammar-pure.
const CAPTURE_KEY_NAMES: Record<string, string> = { " ": "Space" };

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
 * and plain Escape cancels; either key WITH a modifier records as a chord.
 * The FIRST non-modifier press records the chord; later presses are ignored
 * (a stray key before Enter must not silently replace what is saved) - to
 * change the chord, cancel and re-capture. */
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
    // IME composition keydowns are not presses: during composition events
    // arrive with isComposing set and key "Process"/"Unidentified", and an
    // IME COMMIT Enter would otherwise hit the save branch below. The shared
    // guard (keybindings/dispatcher.ts's isIMECompositionKeydown, with its
    // keyCode 229 fallback for browsers that report IME input only through
    // it) precedes preventDefault/stopPropagation, and the dispatcher's own
    // guard covers the propagation we let through. "Process"/"Unidentified"
    // stay local to capture: they must never be RECORDED as a chord.
    if (isIMECompositionKeydown(event.nativeEvent) || event.key === "Process" || event.key === "Unidentified") return;
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
    // The recorded chord stands: further non-modifier presses are consumed
    // (preventDefault/stopPropagation above) but do NOT overwrite it.
    if (captured !== null) return;
    setError(null);
    // ASCII letters normalize to uppercase: KeyboardEvent.key for a
    // mod chord arrives lowercase ("p" for Ctrl+P) while the default map
    // writes letters uppercase, and tinykeys matches case-insensitively, so
    // normalizing keeps display and serialization consistent with the
    // existing rows without changing what matches. The test is ASCII-only on
    // purpose: String.toUpperCase can CHANGE LENGTH on non-ASCII input
    // ("ß".toUpperCase() === "SS"), which would corrupt the chord.
    const key = /^[a-z]$/.test(event.key) ? event.key.toUpperCase() : event.key;
    setCaptured({
      modifiers,
      optionalModifiers: [],
      key: CAPTURE_KEY_NAMES[key] ?? key,
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
  // A failed save leaves this row's inline error AND the store's hubError
  // set. When a later confirmed hub payload (refresh or notification) clears
  // hubError, the row's stale error clears with it - the bindings it
  // described are no longer the truth, and the store's isEditable gate
  // already reopens. Local state only: an unrelated capture is untouched.
  const hubError = useKeybindingsStore((s) => s.hubError);
  useEffect(() => {
    if (hubError === null) setError(null);
  }, [hubError]);
  // Finding 33: preflight and generation-fence rejections deliberately never
  // set hubError, so the hubError transition above cannot clear a row error
  // they left behind. Every confirmed hub payload (refresh, notification, or
  // a later successful patch) supersedes the bindings that error described,
  // so clear on the apply serial instead.
  const appliedSerial = useKeybindingsStore((s) => s.appliedSerial);
  // biome-ignore lint/correctness/useExhaustiveDependencies: appliedSerial is a deliberate trigger-only dep - the effect's whole job is to re-run on the serial bump
  useEffect(() => {
    setError(null);
  }, [appliedSerial]);
  // Reset availability derives from the hub's RAW rules, not the validated
  // set: a persisted rule validation skips (reserved/malformed/conflicting)
  // never reaches `overrides`, but dropping it is exactly the meaningful
  // action - replacementRules composes from rawOverrides for the same
  // reason. The Customized marker stays on the effective bindings.
  const hasOverride = useKeybindingsStore((s) => s.rawOverrides.some((rule) => rule.action === actionId));
  const chordButtonRef = useRef<HTMLButtonElement>(null);
  // Set when a capture ends from the keyboard (Escape cancel, Enter save):
  // focus returns to the chord button so the keyboard flow continues where
  // it started. A click-away cancel does NOT set it - focus already went
  // where the user put it.
  const refocusOnCloseRef = useRef(false);
  // A save is a hub round trip and OUTLIVES the capture that started it: a
  // click-away cancel closes the box while the PATCH is in flight, and the
  // user can open a NEW capture before the old save resolves. Every capture
  // open/cancel bumps this generation; a resolving save applies its result
  // (closing the box, clearing the row error, refocusing) only while its
  // generation is still current, so a stale continuation cannot close or
  // repaint a capture it did not start. Same role as CaptureBox's
  // savingRef, one level up.
  const captureGenerationRef = useRef(0);

  useEffect(() => {
    if (!capturing && refocusOnCloseRef.current) {
      refocusOnCloseRef.current = false;
      chordButtonRef.current?.focus();
    }
  }, [capturing]);

  // A capture must not outlive editability: a disconnect, support loss, or
  // hub replacement mid-capture would strand an interactive box in a
  // read-only section (the store side already rejects the save). Cancel
  // like a click-away: bump the generation so an in-flight save's
  // continuation no-ops, and do NOT refocus - the controls focus would
  // return to are going away with editability.
  useEffect(() => {
    if (!editable && capturing) {
      captureGenerationRef.current += 1;
      refocusOnCloseRef.current = false;
      setCapturing(false);
    }
  }, [editable, capturing]);

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
    const generation = captureGenerationRef.current;
    try {
      // A THUNK: the store serializes writes, so this composition must run
      // when the write executes (against the raw set the previous write left
      // behind), not now - composing at call time would race an in-flight
      // edit from another row with a stale expectedRevision and payload.
      await keybindingsStore.getState().patchOverrides(() => replacementRules(actionId, serializeChord([chord])));
    } catch (saveError) {
      return errorMessage(saveError);
    }
    // Cancelled (or cancelled-and-reopened) while the PATCH was in flight:
    // the capture this save belongs to is gone; its resolution must not
    // touch the row's current capture state.
    if (captureGenerationRef.current !== generation) return null;
    refocusOnCloseRef.current = true;
    setCapturing(false);
    setError(null);
    return null;
  }

  function cancelCapture(refocus: boolean): void {
    captureGenerationRef.current += 1;
    refocusOnCloseRef.current = refocus;
    setCapturing(false);
  }

  async function applyRules(rules: readonly OverrideRule[] | (() => readonly OverrideRule[])): Promise<void> {
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
        {binding.chord.map((press, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: repeated presses in a multi-press sequence ("g g") are content-identical, position is identity, and the list is static per binding
          <KeyHint key={`${i}:${serializeChord([press])}`} keys={chordDisplayKeys(press)} />
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
              captureGenerationRef.current += 1;
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
              onClick={() => void applyRules(() => replacementRules(actionId, null))}
            >
              Unbind
            </button>
          )}
          {hasOverride && (
            <button
              type="button"
              className={CLASS.actionButton}
              onClick={() => void applyRules(() => replacementRules(actionId, undefined))}
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
  const hubLoading = useKeybindingsStore((s) => s.hubLoading);
  const hubError = useKeybindingsStore((s) => s.hubError);
  const loaded = useKeybindingsStore((s) => s.loaded);
  const warnings = useKeybindingsStore((s) => s.warnings);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);
  const characterKeyTriggers = usePrefsStore((s) => s.characterKeyTriggers);
  const editable = hubSupport === "supported" && hubError === null && !hubLoading && loaded;

  let status: string | undefined;
  if (hubSupport === "unknown") {
    status = "Waiting for the hub connection to report keybindings support.";
  } else if (hubSupport === "unsupported") {
    // Finding 35: after an un-apply ROLLBACK the hubError alert says the
    // old overrides are still in effect - the status must not contradict it
    // by claiming the built-in defaults.
    status =
      hubError === null
        ? "This hub does not support synced keybinding overrides. The built-in defaults are in effect."
        : "This hub does not support synced keybinding overrides. The previously synced overrides are still in effect; restoring the built-in defaults is retried on the next connection change.";
  } else if (hubLoading || !loaded) {
    // The stale-hub window: a client replacement reset the loaded state and
    // the new hub's refresh has not landed (or failed - the alert below
    // carries that message). Read-only until the overrides are current.
    status =
      hubError === null
        ? "Loading this hub's synced keybinding overrides; the shortcuts shown are read-only until they arrive."
        : "Editing is unavailable until this hub's synced keybinding overrides load cleanly.";
  } else if (hubError !== null) {
    // A patch or re-refresh failed AFTER a successful load: the listing is
    // current, but editing waits for a clean reload (the alert carries the
    // failure itself).
    status = "Editing is unavailable until this hub's synced keybinding overrides load cleanly.";
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
          {hubSupport === "supported" && (
            // Finding 37: the round-3 gate locks editing while hubError is
            // set, and a successful refresh clears it - offer the recovery
            // inline. Supported hubs only: on an unsupported hub a refresh
            // cannot help (refreshFor's entry guard refuses it), and the
            // rollback message's own retry is connection-change-driven.
            // Disabled while hubLoading so a double-click cannot double-fire
            // the refresh.
            <button
              type="button"
              className={CLASS.actionButton}
              disabled={hubLoading}
              onClick={() => void keybindingsStore.getState().refreshOverrides()}
            >
              Retry
            </button>
          )}
        </p>
      )}
      {warnings.length > 0 && (
        <div className={CLASS.status} role="status">
          <p>
            {warnings.some((warning) => warning.reason === "character-key-conflict")
              ? "Keybinding warnings:"
              : "Some saved overrides were skipped:"}
          </p>
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
