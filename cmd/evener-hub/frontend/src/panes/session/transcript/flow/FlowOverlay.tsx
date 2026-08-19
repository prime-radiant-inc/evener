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
import type { ReactNode } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./flowoverlay.module.css";

export interface FlowOverlayProps {
  children: ReactNode;
  top?: ReactNode;
  pill?: ReactNode;
}

const CLASS = {
  wrap: requireClass(styles.wrap, "flowoverlay.module.css", "wrap"),
  top: requireClass(styles.top, "flowoverlay.module.css", "top"),
  pillSlot: requireClass(styles.pillSlot, "flowoverlay.module.css", "pillSlot"),
};

export function FlowOverlay({ children, top, pill }: FlowOverlayProps) {
  return (
    <div className={CLASS.wrap}>
      {children}
      {top !== undefined && (
        <div data-testid="flow-overlay-top" className={CLASS.top}>
          {top}
        </div>
      )}
      {pill !== undefined && (
        <div data-testid="flow-overlay-pill" className={CLASS.pillSlot}>
          {pill}
        </div>
      )}
    </div>
  );
}
