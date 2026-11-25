// FlowOverlay: the position:relative frame flow/'s chrome (the load-older
// row, the new-content pill) floats over the virtualized transcript in,
// without touching session.module.css's layout at all - the wrap here IS
// the positioning context, established once, entirely inside this stream's
// own file. `top` floats non-scrolling content above the list (a quiet
// strip); `pill` floats the new-content affordance centered near the
// bottom. Both are position:absolute so neither one ever reflows
// VirtualList's own full-height scroll area - which is also exactly why
// nothing placed here reserves any layout space of its own: `top` has no
// background and no height, so it floats OVER whatever the scroll area
// happens to be showing rather than displacing it. That is fine for the
// load-older row (a brief, self-contained affordance pinned to the true
// top of history) but was wrong for the liveness line, which used to live
// here and collided with real transcript text (kata x47h) - it now renders
// in the pane's reserved footer instead (see Session.tsx).
import { type ReactNode, useLayoutEffect, useRef } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./flowoverlay.module.css";

export interface FlowOverlayProps {
  children: ReactNode;
  top?: ReactNode;
  pill?: ReactNode;
  attentionOverlay?: ReactNode;
  onOverlayHeightChange?: (height: number) => void;
}

const CLASS = {
  wrap: requireClass(styles.wrap, "flowoverlay.module.css", "wrap"),
  top: requireClass(styles.top, "flowoverlay.module.css", "top"),
  stack: requireClass(styles.stack, "flowoverlay.module.css", "stack"),
  pillSlot: requireClass(styles.pillSlot, "flowoverlay.module.css", "pillSlot"),
  attentionSlot: requireClass(styles.attentionSlot, "flowoverlay.module.css", "attentionSlot"),
};

export function FlowOverlay({ children, top, pill, attentionOverlay, onOverlayHeightChange }: FlowOverlayProps) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const stackRef = useRef<HTMLDivElement>(null);
  const reportRef = useRef(onOverlayHeightChange);
  reportRef.current = onOverlayHeightChange;
  const hasStack = pill !== undefined || attentionOverlay !== undefined;

  // The last turn must scroll above both the controls and their bottom inset.
  // biome-ignore lint/correctness/useExhaustiveDependencies: hasStack attaches the observer when the stack mounts and clears the reserve when it unmounts
  useLayoutEffect(() => {
    const wrap = wrapRef.current;
    const stack = stackRef.current;
    if (!wrap || !stack) {
      reportRef.current?.(0);
      return;
    }
    // A rect, not offsetHeight: the stack has no scale animation, and rects
    // stay honest in jsdom where the shared offsetHeight test stub is 500 for
    // every element and an empty stack must report no clearance at all.
    const report = () => reportRef.current?.(wrap.getBoundingClientRect().bottom - stack.getBoundingClientRect().top);
    report();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(report);
    observer.observe(stack, { box: "border-box" });
    return () => observer.disconnect();
  }, [hasStack]);

  return (
    <div ref={wrapRef} className={CLASS.wrap}>
      {children}
      {top !== undefined && (
        <div data-testid="flow-overlay-top" className={CLASS.top}>
          {top}
        </div>
      )}
      {hasStack && (
        <div ref={stackRef} data-testid="flow-overlay-stack" className={CLASS.stack}>
          {pill !== undefined && (
            <div data-testid="flow-overlay-pill" className={CLASS.pillSlot}>
              {pill}
            </div>
          )}
          {attentionOverlay !== undefined && (
            <div data-testid="flow-overlay-attention" className={CLASS.attentionSlot}>
              {attentionOverlay}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
