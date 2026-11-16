// iOS Safari's half of the no-touch-zoom decision (Jesse 2026-11-15).
// global.css's `(any-pointer: coarse) touch-action: pan-x pan-y` blocks
// pinch zoom in Chromium and on Android, but WebKit does not honor
// touch-action for viewport pinch — the same accessibility stance that
// makes it ignore maximum-scale — so the shipped CSS left the iPhone
// zoomable (Jesse's Duo test, 2026-11-15, after PR #6470). What Safari
// does respect is its non-standard gesture events: a touch pinch begins
// with a cancelable gesturestart on the document (gesturechange follows
// as it grows), and preventing them vetoes the zoom. Safari has also been
// reported to fire gesture events for desktop trackpad pinches, where
// zoom must keep working, so the veto installs only when the
// coarse-pointer query says a touch screen is present — the same gate
// the CSS rule uses, and one no desktop Mac passes. gesturestart is the
// load-bearing veto; gesturechange is insurance for Safari builds that
// grow the zoom without it.
const GESTURE_EVENT_NAMES = ["gesturestart", "gesturechange"] as const;

/** The window surface the gate consults; structural so tests can fake it. */
type PinchVetoWindow = {
  matchMedia(query: string): { matches: boolean };
  document: Document;
};

/**
 * Registers the Safari pinch veto on `doc` and returns its remover. The
 * listeners are explicitly non-passive: a passive listener cannot
 * preventDefault, and the pinch would silently still zoom.
 */
export function installPinchVeto(doc: Document): () => void {
  const veto = (event: Event): void => {
    event.preventDefault();
  };
  const options: AddEventListenerOptions = { passive: false };
  for (const type of GESTURE_EVENT_NAMES) {
    doc.addEventListener(type, veto, options);
  }
  return () => {
    for (const type of GESTURE_EVENT_NAMES) {
      doc.removeEventListener(type, veto);
    }
  };
}

/**
 * Installs the pinch veto only where a touch screen exists. Desktop
 * Safari's zoom paths (trackpad pinch included) must keep working, and
 * the coarse-pointer query is what separates the two worlds.
 */
export function installTouchPinchVeto(win: PinchVetoWindow): () => void {
  if (!win.matchMedia("(any-pointer: coarse)").matches) {
    return () => {};
  }
  return installPinchVeto(win.document);
}
