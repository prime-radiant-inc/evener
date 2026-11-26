import { type RefObject, useId, useLayoutEffect, useRef, useState } from "react";
import { hasIPhoneWorkspaceControls, iPhoneCommand, tellIPhoneHost } from "../iPhoneHost";

/** Keep the web control and its normal click action until iOS has mounted the native button. */
export function useIPhoneCreateButton(rail: RefObject<HTMLDivElement | null>) {
  const ref = useRef<HTMLButtonElement>(null);
  const id = useId();
  const [shown, setShown] = useState(false);
  useLayoutEffect(() => {
    const button = ref.current;
    const board = rail.current;
    if (!button || !board || !hasIPhoneWorkspaceControls()) return;
    const viewport = window.visualViewport;
    const drawer = button.closest('[role="dialog"]');
    function frame() {
      if (!button || button.closest('[inert], [data-motion="opening"]') || (viewport && viewport.scale !== 1))
        return null;
      // A fully visible button can still be moving with the Sessions drawer.
      if (drawer?.getAnimations().some((animation) => animation.playState === "running")) return null;
      const layer = document.activeElement?.closest(
        '[data-focus-layer], [aria-modal="true"], [role="menu"], [role="listbox"]',
      );
      if (layer && !layer.contains(button)) return null;
      const { left: x, top: y, width, height } = button.getBoundingClientRect();
      if (!width || !height || !button.contains(document.elementFromPoint(x + width / 2, y + height / 2))) return null;
      return { x, y, width, height };
    }
    let previous = "";
    function measure() {
      const rect = frame();
      const key = JSON.stringify(rect);
      if (previous === key) return;
      previous = key;
      if (!rect) setShown(false);
      tellIPhoneHost({ type: "createButton", id, frame: rect });
    }
    function receive(event: MessageEvent) {
      const command = iPhoneCommand(event);
      if (command?.id !== id) return;
      if (command.type === "createButtonShown") setShown(!!frame());
      if (command.type === "createSession" && frame()) button?.click();
    }
    function scroll(event: Event) {
      if (!(event.target instanceof Element) || event.target.contains(button)) measure();
    }
    // Scroll can move a sticky control without resizing it. Overlays may
    // instead obscure its existing frame when focus or modal ownership changes.
    const resize = new ResizeObserver(measure);
    resize.observe(board);
    resize.observe(button);
    const scrollBody = button.parentElement?.parentElement;
    if (scrollBody) resize.observe(scrollBody);
    const intersection = new IntersectionObserver(measure, { threshold: [0, 1] });
    intersection.observe(button);
    const layers = new MutationObserver(measure);
    layers.observe(document.body, {
      subtree: true,
      attributes: true,
      attributeFilter: ["inert", "aria-modal", "data-motion"],
    });
    window.addEventListener("message", receive);
    window.addEventListener("scroll", scroll, true);
    document.addEventListener("focusin", measure);
    document.addEventListener("focusout", measure);
    drawer?.addEventListener("animationstart", measure);
    drawer?.addEventListener("animationend", measure);
    drawer?.addEventListener("animationcancel", measure);
    viewport?.addEventListener("resize", measure);
    viewport?.addEventListener("scroll", measure);
    measure();
    return () => {
      resize.disconnect();
      intersection.disconnect();
      layers.disconnect();
      window.removeEventListener("message", receive);
      window.removeEventListener("scroll", scroll, true);
      document.removeEventListener("focusin", measure);
      document.removeEventListener("focusout", measure);
      drawer?.removeEventListener("animationstart", measure);
      drawer?.removeEventListener("animationend", measure);
      drawer?.removeEventListener("animationcancel", measure);
      viewport?.removeEventListener("resize", measure);
      viewport?.removeEventListener("scroll", measure);
      tellIPhoneHost({ type: "createButton", id, frame: null });
    };
  }, [id, rail]);
  return { ref, shown };
}
