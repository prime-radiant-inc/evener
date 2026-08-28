// iOS Safari's half of the on-screen-keyboard fix (the viewport meta's
// interactive-widget=resizes-content covers Chromium/Android; Safari ignores
// that key). When the keyboard opens, Safari shrinks only the VISUAL viewport
// — the layout viewport the fixed mobile shell (AppShell.module.css's
// position:fixed; inset:0) is sized against stays full height, so the composer
// docked in the shell's footer ends up underneath the keyboard. This hook
// mirrors the occluded bottom strip into a --keyboard-inset CSS variable that
// the mobile .shell rule spends as padding-bottom, riding the composer up
// exactly with the keyboard and back down when it closes.
import { useEffect } from "react";

const VAR_NAME = "--keyboard-inset";

/**
 * Bottom pixels of the layout viewport occluded by the on-screen keyboard (or
 * any other visual-viewport shrink, e.g. Safari's collapsing toolbars).
 * offsetTop is subtracted because the visual viewport may also be scrolled
 * away from the layout viewport's top edge; only the remainder hangs past its
 * bottom. Pinch zoom (scale > 1) shrinks the visual viewport without occluding
 * anything, so it reports 0 — and the pinned viewport meta keeps scale at 1 in
 * practice, so this is a guard, not the mechanism.
 */
export function keyboardInset(vv: Pick<VisualViewport, "height" | "offsetTop" | "scale">, innerHeight: number): number {
  if (Math.abs(vv.scale - 1) > 0.01) return 0;
  return Math.max(0, innerHeight - vv.height - vv.offsetTop);
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
      // only a real change earns the style recalc.
      const next = `${keyboardInset(vv, window.innerHeight)}px`;
      if (next === lastValue) return;
      lastValue = next;
      root.style.setProperty(VAR_NAME, next);
    };
    apply();
    vv.addEventListener("resize", apply);
    vv.addEventListener("scroll", apply);
    return () => {
      vv.removeEventListener("resize", apply);
      vv.removeEventListener("scroll", apply);
      root.style.removeProperty(VAR_NAME);
    };
  }, []);
}
