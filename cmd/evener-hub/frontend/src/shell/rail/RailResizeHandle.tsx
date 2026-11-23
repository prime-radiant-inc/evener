// The drag-to-resize handle: a role=separator strip on the right edge of
// whatever it is given, over the border it appears to grab. Two surfaces
// render it: the desktop's docked rail (RailHost, painting --rail-width from
// the sidebarWidth pref) and the tablet split's board column (StackHost,
// painting --board-width from the boardWidth pref, where a reset returns
// the column to its auto clamp rather than a default number). Every
// parameterized prop defaults to the rail's exact behavior, so the rail's
// own tests are the default path's contract.
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
import { clampSidebarWidth, SIDEBAR_WIDTH_DEFAULT, SIDEBAR_WIDTH_MAX, sidebarWidthMin } from "../../stores/prefs";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./RailResizeHandle.module.css";

const CLASS = {
  resizeHandle: requireClass(styles.resizeHandle, "RailResizeHandle.module.css", "resizeHandle"),
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
  // The split's column may be auto (null): no aria-valuenow until a drag or
  // step paints one, and a keyboard step bases itself on the element's
  // measured width instead.
  width: number | null;
  onCommit: (width: number) => void;
  // The element being resized: the drag paints its --rail-width directly.
  railRef: RefObject<HTMLDivElement | null>;
  // The custom property the drag paints. Defaults to the rail's own.
  property?: string;
  // The width clamp for this surface. Defaults to the rail's own.
  clamp?: (value: number) => number;
  // This surface's aria-valuemax (the split's ceiling is the viewport, not
  // the static sidebar max). Defaults to the rail's own.
  ariaMax?: number;
  // Present when a double-click resets to a non-number state: the split's
  // auto clamp. Absent, the double-click restores SIDEBAR_WIDTH_DEFAULT.
  onReset?: () => void;
  // The test id, so two surfaces' handles never share one. Defaults to the
  // rail's own.
  testId?: string;
}

export function RailResizeHandle({
  width,
  onCommit,
  railRef,
  property = RAIL_WIDTH_PROPERTY,
  clamp = clampSidebarWidth,
  ariaMax = SIDEBAR_WIDTH_MAX,
  onReset,
  testId = "rail-resize-handle",
}: RailResizeHandleProps) {
  // Non-null only mid-drag; the live value aria-valuenow reports.
  const [dragWidth, setDragWidth] = useState<number | null>(null);
  const shownWidth = dragWidth ?? width;

  function paint(value: number): void {
    railRef.current?.style.setProperty(property, `${value}px`);
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
    setDragWidth(clamp(rail.getBoundingClientRect().width));
  }

  function handlePointerMove(event: PointerEvent<HTMLDivElement>): void {
    if (dragWidth === null) return;
    const rail = railRef.current;
    if (!rail) return;
    // Measured from the rail's own left edge every move rather than from a
    // captured start offset, so the handle tracks the pointer exactly even if
    // something else (a font-size pref change, a scrollbar) shifts the rail
    // mid-drag.
    const next = clamp(event.clientX - rail.getBoundingClientRect().left);
    setDragWidth(next);
    paint(next);
  }

  function endDrag(event: PointerEvent<HTMLDivElement>): void {
    if (dragWidth === null) return;
    event.currentTarget.releasePointerCapture(event.pointerId);
    setDragWidth(null);
    onCommit(dragWidth);
  }

  // An auto (null) width bases a step on the element's measured width - the
  // same reading a drag's pointerdown takes - so the first step moves from
  // where the column actually is.
  function stepBase(): number {
    return shownWidth ?? clamp(railRef.current?.getBoundingClientRect().width ?? SIDEBAR_WIDTH_DEFAULT);
  }

  function step(delta: number): void {
    const next = clamp(stepBase() + delta);
    paint(next);
    onCommit(next);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    // Alt/Ctrl/Meta mean the key belongs elsewhere (Alt+Arrow/Alt+Home/End are
    // the global session/transcript chords); a blanket preventDefault here
    // would swallow them while the handle has focus (roborev PR #884 round
    // 3). Shift stays: it selects the coarse step below.
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const coarse = event.shiftKey ? KEY_STEP_COARSE : KEY_STEP;
    switch (event.key) {
      case "ArrowLeft":
        step(-coarse);
        break;
      case "ArrowRight":
        step(coarse);
        break;
      case "Home":
        step(sidebarWidthMin() - stepBase());
        break;
      case "End":
        step(ariaMax - stepBase());
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
      data-testid={testId}
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      aria-valuenow={shownWidth ?? undefined}
      aria-valuemin={sidebarWidthMin()}
      aria-valuemax={ariaMax}
      tabIndex={0}
      className={CLASS.resizeHandle}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onKeyDown={handleKeyDown}
      onDoubleClick={() => {
        if (onReset) {
          railRef.current?.style.removeProperty(property);
          onReset();
        } else {
          paint(SIDEBAR_WIDTH_DEFAULT);
          onCommit(SIDEBAR_WIDTH_DEFAULT);
        }
      }}
    />
  );
}
