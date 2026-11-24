import { type RefObject, useEffect } from "react";
import { hasFineHoverPointer } from "./useIsMobile";

// useMountAutofocus moves keyboard focus into the referenced element once,
// when its pane loads: writing is what the session and new-session panes are
// for, so the caret starts in the prompt rather than on whichever field
// happens to be first in the DOM.
//
// Mount-only by design - a later `enabled` flip is a pane switch, not a
// load, so it must never yank focus. Gated on `enabled` (the pane being the
// workspace's focused one at mount, so a background tab never steals focus)
// and on a fine hover pointer (a touch device must never pop the on-screen
// keyboard on load, and a landscape phone is wider than the 899px breakpoint
// while still being a phone). The capability gate reads matchMedia once
// rather than subscribing: a mount-only decision needs the value once, not a
// listener.
/** Whether a pane mount will move focus into the referenced field. The
 * composer reads the same gate to start its card in the expanded shape
 * instead of painting folded and then animating open. */
export function mountAutofocusEnabled(enabled: boolean): boolean {
  return enabled && hasFineHoverPointer();
}

export function useMountAutofocus(targetRef: RefObject<{ focus(): void } | null>, enabled: boolean): void {
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only by design - later enabled flips are pane switches, not loads
  useEffect(() => {
    if (!mountAutofocusEnabled(enabled)) return;
    targetRef.current?.focus();
  }, []);
}
