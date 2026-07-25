// The docked rail's drag-to-resize handle: a role=separator strip on the
// rail's right edge, over the border it appears to grab. Desktop only —
// RailHost renders it (and the width it reports) exclusively on the non-mobile
// path, because the mobile Rail fills TreeDrawer's sheet and has no edge to
// drag (Rail.module.css's own <=899px block owns that width).
//
// TWO WRITE PATHS, deliberately:
//   - during a drag, each pointermove sets the --rail-width custom property
//     straight on the rail element through railRef. No store write and no
//     React render of the rail's tree per frame — a session tree can be
//     hundreds of rows, and every pref write hits localStorage.
//   - on pointerup (and on every keyboard step, which is one discrete
//     gesture) onCommit writes the persisted width, and the normal React
//     render puts back the identical inline value the drag already painted.
// aria-valuenow tracks the live width through this leaf component's own
// state, so assistive tech sees the drag as it happens; re-rendering one
// empty div per frame is free.
import { type KeyboardEvent, type PointerEvent, type RefObject, useState } from "react";
import { clampSidebarWidth, SIDEBAR_WIDTH_DEFAULT, SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_MIN } from "../../stores/prefs";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./Rail.module.css";

const CLASS = {
  resizeHandle: requireClass(styles.resizeHandle, "Rail.module.css", "resizeHandle"),
};

// The custom property Rail.module.css's own `.rail` width reads. Set inline by
// Rail on every render, and imperatively by the drag below.
export const RAIL_WIDTH_PROPERTY = "--rail-width";

// One arrow key nudges by the layout grid's own step (var(--space-4) = 16px);
// Shift covers ground faster for a keyboard-only resize of the full range.
const KEY_STEP = 16;
const KEY_STEP_COARSE = 64;

export interface RailResizeHandleProps {
  // The persisted width, and where a released drag / keyboard step lands.
  width: number;
  onCommit: (width: number) => void;
  // The element being resized: the drag paints its --rail-width directly.
  railRef: RefObject<HTMLDivElement | null>;
}

export function RailResizeHandle({ width, onCommit, railRef }: RailResizeHandleProps) {
  // Non-null only mid-drag; the live value aria-valuenow reports.
  const [dragWidth, setDragWidth] = useState<number | null>(null);
  const shownWidth = dragWidth ?? width;

  function paint(value: number): void {
    railRef.current?.style.setProperty(RAIL_WIDTH_PROPERTY, `${value}px`);
  }

  function handlePointerDown(event: PointerEvent<HTMLDivElement>): void {
    if (event.button !== 0) return; // left button only: a right-click must still reach the context menu
    const rail = railRef.current;
    if (!rail) return;
    // Pointer capture, not a window-level mousemove listener: the browser
    // routes every subsequent move/up for this pointer to this element even
    // when the cursor outruns it, crosses the dockview host beside it, or
    // leaves the window entirely — no listener add/remove bookkeeping, and no
    // way for a pane's own handlers to swallow the drag.
    event.currentTarget.setPointerCapture(event.pointerId);
    event.preventDefault(); // suppress the text-selection drag the gesture would otherwise start
    setDragWidth(clampSidebarWidth(rail.getBoundingClientRect().width));
  }

  function handlePointerMove(event: PointerEvent<HTMLDivElement>): void {
    if (dragWidth === null) return;
    const rail = railRef.current;
    if (!rail) return;
    // Measured from the rail's own left edge every move rather than from a
    // captured start offset, so the handle tracks the pointer exactly even if
    // something else (a font-size pref change, a scrollbar) shifts the rail
    // mid-drag.
    const next = clampSidebarWidth(event.clientX - rail.getBoundingClientRect().left);
    setDragWidth(next);
    paint(next);
  }

  function endDrag(event: PointerEvent<HTMLDivElement>): void {
    if (dragWidth === null) return;
    event.currentTarget.releasePointerCapture(event.pointerId);
    setDragWidth(null);
    onCommit(dragWidth);
  }

  function step(delta: number): void {
    const next = clampSidebarWidth(shownWidth + delta);
    paint(next);
    onCommit(next);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    const coarse = event.shiftKey ? KEY_STEP_COARSE : KEY_STEP;
    switch (event.key) {
      case "ArrowLeft":
        step(-coarse);
        break;
      case "ArrowRight":
        step(coarse);
        break;
      case "Home":
        step(SIDEBAR_WIDTH_MIN - shownWidth);
        break;
      case "End":
        step(SIDEBAR_WIDTH_MAX - shownWidth);
        break;
      default:
        return; // every other key keeps its default behaviour (Tab moves on, etc.)
    }
    event.preventDefault();
  }

  return (
    // A separator with keyboard operation is a focusable widget, so it takes a
    // real tabIndex — reachable by Tab, not mouse-only. Double-click restores
    // the default width, the same "reset this control" gesture every other
    // resizable pane splitter offers.
    // The rule's suggested <hr> is a non-interactive void element - it cannot host
    // the drag/keyboard handlers a resize separator needs, so role=separator on a
    // div is the only option.
    // biome-ignore lint/a11y/useSemanticElements: see the comment directly above
    <div
      data-testid="rail-resize-handle"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      aria-valuenow={shownWidth}
      aria-valuemin={SIDEBAR_WIDTH_MIN}
      aria-valuemax={SIDEBAR_WIDTH_MAX}
      tabIndex={0}
      className={CLASS.resizeHandle}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onKeyDown={handleKeyDown}
      onDoubleClick={() => {
        paint(SIDEBAR_WIDTH_DEFAULT);
        onCommit(SIDEBAR_WIDTH_DEFAULT);
      }}
    />
  );
}
