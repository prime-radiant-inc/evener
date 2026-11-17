// The visibility gate the rendering clocks share. A redraw timer buys nothing
// while the tab is hidden: the browser throttles it anyway and nobody reads
// the result. These timers stop on `visibilitychange` to hidden and, on
// becoming visible, restart their cadence with one immediate tick, so a
// relative age that went stale in the background is corrected on the first
// frame the reader sees rather than a full interval later.
//
// This is NOT the pattern for a timer whose job is to make progress rather
// than to redraw. The OAuth device flow and device pairing poll a remote
// server for an approval that outlives the tab, and the mutation outbox's scan
// is durability work; stopping any of them while hidden would strand a
// sign-in or delay a queued mutation. Those keep running.
//
// Visibility is read from the module document, like every other shell-observer
// hook here (useIsMobile's window, markSeen's document). A pane in a Dockview
// popout pauses when the opener's tab hides rather than its own window's; the
// browser hides both together on a tab switch, which is the case this exists
// for.
import { useEffect, useRef } from "react";

/**
 * Run `onTick` every `intervalMs` while the document is visible, and once
 * immediately whenever the document becomes visible again. Nothing ticks at
 * mount (a caller that wants an immediate read does it itself), and nothing
 * ticks while the document is hidden. `enabled` false disarms the timer
 * entirely, as for a clock that only runs while it has live rows to draw.
 */
export function useVisibleInterval(onTick: () => void, intervalMs: number, enabled = true): void {
  // The callback usually closes over component state and callers pass an
  // inline arrow, so a ref keeps an ordinary re-render from restarting the
  // cadence.
  const onTickRef = useRef(onTick);
  onTickRef.current = onTick;
  useEffect(() => {
    if (!enabled) return undefined;
    let timer: ReturnType<typeof setInterval> | undefined;
    const arm = (): void => {
      if (timer !== undefined) return;
      timer = setInterval(() => onTickRef.current(), intervalMs);
    };
    const disarm = (): void => {
      if (timer === undefined) return;
      clearInterval(timer);
      timer = undefined;
    };
    if (document.visibilityState === "hidden") disarm();
    else arm();
    const onVisibilityChange = (): void => {
      if (document.visibilityState === "hidden") {
        disarm();
        return;
      }
      // A resume is the one case that ticks now: the clock was paused, so the
      // next scheduled tick would be a whole interval late. A visibilitychange
      // while already running (a duplicate event) changes nothing.
      const wasPaused = timer === undefined;
      arm();
      if (wasPaused) onTickRef.current();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disarm();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [intervalMs, enabled]);
}
