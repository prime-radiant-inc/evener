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
// Visibility is read from the document the caller names, never from the module
// global, because a Dockview popout is a separate window with its own
// visibility: a pane moved into a popout must keep drawing while the opener's
// tab is hidden. Pane-bound callers resolve that document from the pane (see
// shell/paneDocument.ts and widgets/internal/useOwnedDocument.ts); shell chrome
// that only ever lives on the opener leaves the default.
import { useEffect, useRef } from "react";

/**
 * Run `onTick` every `intervalMs` while `ownerDocument` is visible, and once
 * immediately whenever it becomes visible again. Nothing ticks at mount (a
 * caller that wants an immediate read does it itself), and nothing ticks while
 * the document is hidden. `enabled` false disarms the timer entirely, as for a
 * clock that only runs while it has live rows to draw. `ownerDocument` defaults
 * to the module document, which is right for shell chrome and for a pane that
 * is not in a popout.
 */
export function useVisibleInterval(
  onTick: () => void,
  intervalMs: number,
  enabled = true,
  ownerDocument: Document = document,
): void {
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
    if (ownerDocument.visibilityState === "hidden") disarm();
    else arm();
    const onVisibilityChange = (): void => {
      if (ownerDocument.visibilityState === "hidden") {
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
    ownerDocument.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disarm();
      ownerDocument.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [intervalMs, enabled, ownerDocument]);
}
