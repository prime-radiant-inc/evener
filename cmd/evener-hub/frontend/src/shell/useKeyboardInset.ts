// On-screen-keyboard inset for browsers that keep the layout viewport full
// height while shrinking only the visual viewport. Browsers that honor
// index.html's interactive-widget=resizes-content (Chromium/Android, Safari
// 27.3+) shrink the layout viewport itself so the fixed mobile shell
// (AppShell.module.css's position:fixed; inset:0) rides up with the keyboard
// and this hook reports 0px. When the layout viewport stays full height, this
// hook mirrors the occluded bottom strip into --keyboard-inset so the mobile
// .shell rule spends it as padding-bottom.
import { useEffect } from "react";

const VAR_NAME = "--keyboard-inset";

function isPinchZoomed(vv: Pick<VisualViewport, "scale">): boolean {
  return Math.abs(vv.scale - 1) > 0.01;
}

/**
 * Bottom pixels of the layout viewport occluded by the on-screen keyboard (or
 * any other visual-viewport shrink, e.g. Safari's collapsing toolbars).
 * offsetTop is subtracted because the visual viewport may also be scrolled
 * away from the layout viewport's top edge; only the remainder hangs past its
 * bottom. Pinch zoom (scale > 1) shrinks the visual viewport without occluding
 * anything, so it reports 0 — a guard, not the mechanism. Touch screens
 * cannot zoom this app at all (fields hold the 16px floor; touch-action and
 * pinchVeto spend the zoom gestures), so a scale past 1 is a desktop zoom
 * gesture — trackpad pinch still moves it — and the shell that spends the
 * inset is the phone's.
 */
export function keyboardInset(
  vv: Pick<VisualViewport, "height" | "offsetTop" | "scale">,
  layoutHeight: number,
): number {
  // During a display switch WebKit can report a negative visual height.
  // Only a positive viewport can describe which part of the page is visible.
  if (vv.height <= 0 || isPinchZoomed(vv)) return 0;
  return Math.max(0, layoutHeight - vv.height - vv.offsetTop);
}

/**
 * Layout viewport height in CSS pixels. Fixed elements (`position: fixed;
 * inset: 0`) resolve against `documentElement.clientHeight`, whereas
 * `window.innerHeight` can stay at the pre-keyboard height under
 * `interactive-widget=resizes-content` and double-count the keyboard inset.
 * Falls back to `window.innerHeight` when `clientHeight` is 0 (unstyled jsdom).
 */
function layoutViewportHeight(root: HTMLElement): number {
  const height = root.clientHeight;
  return height > 0 ? height : window.innerHeight;
}

export function useKeyboardInset(): void {
  useEffect(() => {
    const vv = window.visualViewport;
    // Guard covers both null (stubbed) and undefined (jsdom, old browsers).
    if (!vv) return undefined;
    const root = document.documentElement;
    let lastValue: string | null = null;
    const apply = (): void => {
      // Scroll events fire per-frame with an almost-always-unchanged value;
      // skip the clientHeight layout read while pinch-zoomed and only write on
      // a real change so redundant events earn no style recalc.
      const inset = isPinchZoomed(vv) ? 0 : keyboardInset(vv, layoutViewportHeight(root));
      const next = `${inset}px`;
      if (next === lastValue) return;
      lastValue = next;
      root.style.setProperty(VAR_NAME, next);
    };
    apply();
    vv.addEventListener("resize", apply);
    vv.addEventListener("scroll", apply);
    // A layout-viewport resize under interactive-widget=resizes-content can
    // commit after visualViewport.resize without firing a second vv resize.
    window.addEventListener("resize", apply);
    return () => {
      vv.removeEventListener("resize", apply);
      vv.removeEventListener("scroll", apply);
      window.removeEventListener("resize", apply);
      root.style.removeProperty(VAR_NAME);
    };
  }, []);
}
