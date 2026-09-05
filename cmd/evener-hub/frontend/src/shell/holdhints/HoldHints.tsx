// The hold-modifier hint chips (Phase 4a, the p4 plan's Design decision 4):
// hold ⌘ (Apple) / Ctrl (elsewhere) alone for ~400ms and a small chip fades
// in over each bound affordance, showing the EFFECTIVE chord for the action
// that affordance triggers. Release - or any of the cleanup paths in
// holdHintsController.ts - hides them.
//
// The chords are registry-sourced through keybindings/display.ts's
// displayBindingFor - the same read the cheatsheet overlay and the Settings
// section make - so a hub-synced override (or an unbound action, whose chip
// then does not render) shows truthfully, and no hand-maintained copy can go
// stale (the survey's stale-HELP_ROWS lesson).
//
// Desktop-only by mount site: AppShell renders this component behind its
// {!isMobile && ...} gate (the CheatsheetOverlay precedent), so a touch
// viewport installs no listeners and renders no chips. The detection
// listeners are observers only (holdHintsController.ts's header).
//
// The chips anchor to REAL elements by the data attributes those components
// carry, queried live at show time - never absolutely-positioned guesses:
// an affordance that is not mounted (the rail's toggle exists in exactly one
// of its two states) simply renders no chip.

import { Fragment, type ReactNode, useEffect } from "react";
import { createPortal } from "react-dom";
import { useStore } from "zustand";
import { ACTIONS } from "../../keybindings/actions";
import { chordDisplayKeys, type KeySequence, serializeChord } from "../../keybindings/chord";
import { displayBindingFor } from "../../keybindings/display";
import { keybindingsRegistry } from "../../keybindings/registry";
import { KeyHint } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { workspaceStore } from "../workspace";
import { installHoldHints, useHoldHintsStore } from "./holdHintsController";
import styles from "./holdhints.module.css";
import { usePrefersReducedMotion } from "./usePrefersReducedMotion";

const CLASS = {
  root: requireClass(styles.root, "holdhints.module.css", "root"),
  chip: requireClass(styles.chip, "holdhints.module.css", "chip"),
  below: requireClass(styles.below, "holdhints.module.css", "below"),
  above: requireClass(styles.above, "holdhints.module.css", "above"),
  or: requireClass(styles.or, "holdhints.module.css", "or"),
};

interface AnchorSpec {
  /** The chip's data-hint value (its stable hook for tests). */
  id: string;
  /** Selects the live affordance element the chip anchors to. */
  selector: string;
  /** The actions whose effective chords the chip shows (a chip for an
   * action with NO effective binding - unbound by override - is skipped). */
  actionIds: readonly string[];
  /** Chips sit BELOW the top-edge affordances (rail header, tab strip) and
   * ABOVE the bottom-edge one (the composer). */
  placement: "above" | "below";
  /** Optional resolver overriding the bare selector query, for affordances
   * whose action targets one of SEVERAL matching elements (the composer). */
  resolve?: () => HTMLElement | null;
}

// resolveFocusedComposer mirrors composer.focus's own target choice
// (AppShell.tsx's registerAction: the FOCUSED session pane's ref): in a
// split workspace several [data-composer] anchors are mounted, and the chip
// must sit over the one the chord would actually focus (roborev PR #884).
// Falls back to the first anchor when no session pane is focused (the chord
// no-ops then, and the pre-change single-composer behavior holds).
function resolveFocusedComposer(): HTMLElement | null {
  const state = workspaceStore.getState();
  const pane = state.panes.find((p) => p.id === state.focusedPaneId);
  const ref = pane?.type === "session" ? (pane.params as { ref?: unknown } | undefined)?.ref : undefined;
  if (typeof ref === "string" && ref !== "") {
    // Dataset equality, not a selector-interpolated ref: no CSS.escape
    // dependency (jsdom has none) and no quoting hazard for odd ref values.
    const targeted = Array.from(document.querySelectorAll<HTMLElement>("[data-composer]")).find(
      (el) => el.dataset.composer === ref,
    );
    if (targeted !== undefined) return targeted;
  }
  return document.querySelector("[data-composer]");
}

// The bound affordances (Design decision 4): the palette trigger (the rail's
// search button - data-search-trigger pre-exists as AppShell's click-to-open
// hook), the rail toggle (Rail's "Hide sidebar" button and RailHost's
// hidden-state ☰ chip, exactly one of which is mounted), the session tabs
// (PaneTab's session wrapper - one chip over the first tab carrying BOTH
// cycling chords), and the composer (Composer's form wrapper).
const ANCHORS: readonly AnchorSpec[] = [
  {
    id: "palette",
    selector: "[data-search-trigger]",
    actionIds: [ACTIONS.paletteOpen],
    placement: "below",
  },
  {
    id: "rail-toggle",
    selector: "[data-rail-toggle]",
    actionIds: [ACTIONS.railToggle],
    placement: "below",
  },
  {
    id: "session-tabs",
    selector: "[data-session-tab]",
    actionIds: [ACTIONS.sessionPrevious, ACTIONS.sessionNext],
    placement: "below",
  },
  {
    id: "composer",
    selector: "[data-composer]",
    actionIds: [ACTIONS.composerFocus],
    placement: "above",
    resolve: resolveFocusedComposer,
  },
];

export function HoldHints() {
  const visible = useHoldHintsStore((s) => s.visible);
  const bindings = useStore(keybindingsRegistry, (s) => s.bindings);
  const reducedMotion = usePrefersReducedMotion();

  // The detection listeners' whole lifetime is this mount (desktop-only, per
  // the file header), so a touch viewport never installs one.
  useEffect(() => installHoldHints(), []);

  if (!visible) return null;

  const chips: ReactNode[] = [];
  for (const spec of ANCHORS) {
    const anchor = spec.resolve ? spec.resolve() : document.querySelector(spec.selector);
    if (!(anchor instanceof HTMLElement)) continue;
    // The WHOLE effective sequence per action (an override can be
    // multi-press); an action with no effective binding contributes nothing.
    const sequences: KeySequence[] = [];
    for (const actionId of spec.actionIds) {
      const sequence = displayBindingFor(bindings, actionId)?.chord;
      if (sequence !== undefined && sequence.length > 0) sequences.push(sequence);
    }
    if (sequences.length === 0) continue;
    const rect = anchor.getBoundingClientRect();
    chips.push(
      <span
        key={spec.id}
        data-hint={spec.id}
        className={`${CLASS.chip} ${spec.placement === "above" ? CLASS.above : CLASS.below}`}
        style={{ left: rect.left + rect.width / 2, top: spec.placement === "above" ? rect.top : rect.bottom }}
      >
        {sequences.map((sequence, i) => (
          <Fragment key={serializeChord(sequence)}>
            {i > 0 && <span className={CLASS.or}>/</span>}
            {sequence.map((press, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: repeated presses in a multi-press sequence ("g g") are content-identical, position is identity, and the list is static per binding
              <KeyHint key={`${i}:${serializeChord([press])}`} keys={chordDisplayKeys(press)} />
            ))}
          </Fragment>
        ))}
      </span>,
    );
  }
  if (chips.length === 0) return null;

  return createPortal(
    <div
      className={CLASS.root}
      data-hold-hints=""
      data-reduced-motion={reducedMotion ? "" : undefined}
      aria-hidden="true"
    >
      {chips}
    </div>,
    document.body,
  );
}
