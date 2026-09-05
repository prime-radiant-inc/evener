// Hold-modifier hint detection (Phase 4a, the p4 plan's Design decision 4):
// holding the primary modifier ALONE - Meta on Apple platforms, Control
// elsewhere, the same platform split as keybindings/validation.ts's
// currentKeybindingsPlatform (and keyhint's Mod glyph) - for
// HOLD_THRESHOLD_MS shows the hint chips; any cleanup path hides them. The
// store mirrors cheatsheetController.ts: a vanilla store the mounted
// <HoldHints/> subscribes to.
//
// The listeners installed here are OBSERVERS ONLY - they never preventDefault
// and never stopPropagation, so the keybindings dispatcher (and every other
// keydown consumer) sees exactly the events it would see without this module.
// That is pinned by HoldHints.test.tsx.
//
// The cleanup set comes from the feasibility research (docs/web-ui/specs/
// 2026-09-03-keybinding-system-survey.md): the modifier's keyup IS delivered
// even on macOS, but a non-modifier keyup while the modifier is held is NOT -
// so release alone cannot be trusted to end every hold. Every one of these
// hides the chips, and each is pinned by a test:
//
//   - the tracked modifier's keyup (the normal path),
//   - window blur (⌘-Tab away mid-hold),
//   - document visibilitychange (tab switch mid-hold),
//   - ANY keydown that is not the tracked modifier (a chord starting - its
//     keyup may never arrive on macOS, so the keydown itself is the hide),
//   - a hard timeout (HARD_TIMEOUT_MS) as the final backstop.
//
// A stuck visible state must be impossible.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { currentKeybindingsPlatform } from "../../keybindings/validation";

/** How long the modifier must be held alone before the chips appear. */
export const HOLD_THRESHOLD_MS = 400;

/** The backstop: the chips can never stay up longer than this, even if every
 * event-based cleanup path missed (a keyup swallowed by an OS-level chord,
 * a blur that never fired). */
export const HARD_TIMEOUT_MS = 10_000;

export interface HoldHintsState {
  visible: boolean;
}

export const holdHintsStore = createStore<HoldHintsState>(() => ({ visible: false }));

export function useHoldHintsStore<T>(selector: (state: HoldHintsState) => T): T {
  return useStore(holdHintsStore, selector);
}

/** Test-only: clears the visible flag between tests. The timers belong to
 * the install disposer, which the component's unmount runs. */
export function resetHoldHintsForTests(): void {
  holdHintsStore.setState({ visible: false });
}

/** Installs the observer listeners; returns the disposer (which also hides
 * and clears every pending timer). Per-mount lifetime: <HoldHints/>'s one
 * effect owns it, and AppShell mounts that component only on desktop, so a
 * touch viewport never installs a listener. */
export function installHoldHints(target: Window = window): () => void {
  const modifierKey = currentKeybindingsPlatform() === "apple" ? "Meta" : "Control";
  // The OTHER modifier flags: the hold counts only while the tracked modifier
  // is held ALONE. A keydown arriving with Shift/Alt (or the other of
  // Meta/Ctrl) already down is the start of a chord, not of a hint hold.
  // (The tracked modifier's own flag is already true on its keydown, so it
  // must not be in this list.)
  const otherFlags: readonly ("ctrlKey" | "metaKey" | "altKey" | "shiftKey")[] =
    modifierKey === "Meta" ? ["ctrlKey", "altKey", "shiftKey"] : ["metaKey", "altKey", "shiftKey"];

  let holdTimer: ReturnType<typeof setTimeout> | null = null;
  let hardTimer: ReturnType<typeof setTimeout> | null = null;

  function clearTimers(): void {
    if (holdTimer !== null) {
      clearTimeout(holdTimer);
      holdTimer = null;
    }
    if (hardTimer !== null) {
      clearTimeout(hardTimer);
      hardTimer = null;
    }
  }

  function hide(): void {
    clearTimers();
    if (holdHintsStore.getState().visible) holdHintsStore.setState({ visible: false });
  }

  function show(): void {
    holdTimer = null;
    holdHintsStore.setState({ visible: true });
    hardTimer = setTimeout(hide, HARD_TIMEOUT_MS);
  }

  function onKeyDown(event: KeyboardEvent): void {
    // Any other key - printable, navigation, or another modifier - ends the
    // hold (and clears a pending one): the hold is "modifier alone", and on
    // macOS this keydown may be the ONLY event the chord ever delivers.
    if (event.key !== modifierKey) {
      hide();
      return;
    }
    // Held-down modifiers auto-repeat on some platforms; the first keydown
    // already armed the timer.
    if (event.repeat) return;
    if (otherFlags.some((flag) => event[flag])) return;
    if (holdTimer !== null || holdHintsStore.getState().visible) return;
    holdTimer = setTimeout(show, HOLD_THRESHOLD_MS);
  }

  function onKeyUp(event: KeyboardEvent): void {
    if (event.key === modifierKey) hide();
  }

  function onBlur(): void {
    hide();
  }

  function onVisibilityChange(): void {
    hide();
  }

  target.addEventListener("keydown", onKeyDown);
  target.addEventListener("keyup", onKeyUp);
  target.addEventListener("blur", onBlur);
  target.document.addEventListener("visibilitychange", onVisibilityChange);

  return () => {
    target.removeEventListener("keydown", onKeyDown);
    target.removeEventListener("keyup", onKeyUp);
    target.removeEventListener("blur", onBlur);
    target.document.removeEventListener("visibilitychange", onVisibilityChange);
    hide();
  };
}
