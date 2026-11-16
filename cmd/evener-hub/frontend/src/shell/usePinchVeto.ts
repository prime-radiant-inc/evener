// iOS Safari's half of the no-touch-zoom decision (Jesse 2026-11-15).
// global.css's `(any-pointer: coarse) touch-action: pan-x pan-y` blocks
// pinch zoom in Chromium and on Android, but WebKit does not honor
// touch-action for viewport pinch — the same accessibility stance that
// makes it ignore maximum-scale — so the shipped CSS left the iPhone
// zoomable (Jesse's Duo test, 2026-11-15, after PR #6470). What Safari
// does respect is its non-standard gesture events: every pinch begins
// with a cancelable gesturestart on the document (gesturechange follows
// as it grows), and preventing them vetoes the zoom. gesturestart and
// gesturechange fire for touch input only — a trackpad or mouse never
// produces one — so no desktop zoom path can be affected by
// construction, the same guarantee the CSS rule carries.
import { useEffect } from "react";

type GestureEventName = "gesturestart" | "gesturechange";

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
  for (const type of ["gesturestart", "gesturechange"] as GestureEventName[]) {
    doc.addEventListener(type, veto, options);
  }
  return () => {
    for (const type of ["gesturestart", "gesturechange"] as GestureEventName[]) {
      doc.removeEventListener(type, veto);
    }
  };
}

export function usePinchVeto(): void {
  useEffect(() => installPinchVeto(document), []);
}
