import {
  type CSSProperties,
  type ReactNode,
  type PointerEvent as ReactPointerEvent,
  useEffect,
  useRef,
  useState,
} from "react";
import dialogStyles from "../dialog/dialog.module.css";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";

export type SheetSide = "right" | "bottom" | "left";
export type SheetSize = "standard" | "wide";

export interface ExpandableConfig {
  peekHeight: number;
  fullScreenFirst?: boolean;
}

export interface SheetProps {
  side?: SheetSide;
  size?: SheetSize;
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** When set, the Sheet renders a drag handle and manages a "peek" | "full"
   * geometry state (full on every open transition when fullScreenFirst). The
   * handle both drags (pointermove threshold) and taps (toggles). Scoped
   * header/body classes flow to OverlayPanel. Defaults to undefined; existing
   * consumers that don't pass it render exactly as before. */
  expandable?: ExpandableConfig;
  /** Optional extra class appended to the panel element after the side and
   * size classes. */
  panelClassName?: string;
  /** Optional extra class appended to the body element (after the shared
   * dialog body class). The mobile sessions drawer uses it to drop the body
   * padding so the rail fills the sheet flush instead of sitting in an inset
   * box (typography-spacing-critique-2026-09-06 finding 9). */
  bodyClassName?: string;
}

const BASE_PANEL_CLASS = requireClass(dialogStyles.panel, "dialog.module.css", "panel");

const SIDE_CLASS: Record<SheetSide, string> = {
  right: `${BASE_PANEL_CLASS} ${requireClass(styles.right, "sheet.module.css", "right")}`,
  bottom: `${BASE_PANEL_CLASS} ${requireClass(styles.bottom, "sheet.module.css", "bottom")}`,
  left: `${BASE_PANEL_CLASS} ${requireClass(styles.left, "sheet.module.css", "left")}`,
};

const SIZE_CLASS: Record<SheetSize, string> = {
  standard: requireClass(styles.standard, "sheet.module.css", "standard"),
  wide: requireClass(styles.wide, "sheet.module.css", "wide"),
};

const HANDLE_CLASS = requireClass(styles.handle, "sheet.module.css", "handle");
const HANDLE_BAR_CLASS = requireClass(styles.handleBar, "sheet.module.css", "handleBar");
const EXPANDABLE_BOTTOM_CLASS = requireClass(styles.expandableBottom, "sheet.module.css", "expandableBottom");
const EXPANDABLE_HEADER_CLASS = requireClass(styles.expandableHeader, "sheet.module.css", "expandableHeader");
const EXPANDABLE_BODY_CLASS = requireClass(styles.expandableBody, "sheet.module.css", "expandableBody");

function DragHandle({ onPointerDown }: { onPointerDown: (e: ReactPointerEvent) => void }) {
  return (
    <div className={HANDLE_CLASS} data-testid="sheet-handle" onPointerDown={onPointerDown}>
      <div className={HANDLE_BAR_CLASS} />
    </div>
  );
}

/**
 * Slide-over panel anchored to the right edge, the left edge, or the
 * bottom edge - otherwise the exact Dialog contract (see ../dialog),
 * sharing its OverlayPanel: scrim, Escape/scrim-click to close, trapped
 * and restored focus, close button. Only the panel's own geometry and
 * slide-in animation differ (sheet.module.css).
 *
 * When `expandable` is set, the Sheet additionally renders a drag handle
 * above the header and manages a "peek" | "full" geometry state, resetting
 * to "full" on every open false->true transition (when fullScreenFirst).
 * The handle drags between the two geometries (pointermove past a threshold
 * on the window) and taps to toggle. The current geometry is surfaced on a
 * wrapper div inside the body as a `data-geometry` attribute for tests and
 * for CSS to key off. When `expandable` is not set, this is the plain
 * Sheet - no handle, no extra classes, fixed geometry - byte-for-byte the
 * pre-existing behavior.
 */
export function Sheet({
  side = "right",
  size = "standard",
  open,
  onClose,
  title,
  children,
  footer,
  expandable,
  panelClassName,
  bodyClassName,
}: SheetProps) {
  const [geometry, setGeometry] = useState<"peek" | "full">(expandable?.fullScreenFirst ? "full" : "peek");
  const dragStartYRef = useRef<number | null>(null);
  const dragStartGeometryRef = useRef<"peek" | "full">("peek");
  // The window pointermove/pointerup listeners added in startDrag are
  // removed in handlePointerUp. If the component unmounts mid-drag (e.g.
  // the panel closes), handlePointerUp never runs and the listeners leak
  // on window. These refs hold the active listener functions so an unmount
  // cleanup can remove them by the same reference that added them.
  const pointerMoveRef = useRef<((e: PointerEvent) => void) | null>(null);
  const pointerUpRef = useRef<((e: PointerEvent) => void) | null>(null);

  // Reset geometry to full on every open transition (false->true). An effect
  // keyed on `open` (and fullScreenFirst) is the only place that observes
  // that transition; the render-time useState initializer only fires once,
  // on mount, so without this a peek reached before close would survive a
  // reopen.
  useEffect(() => {
    if (open && expandable?.fullScreenFirst) setGeometry("full");
  }, [open, expandable?.fullScreenFirst]);

  // Remove any active drag listeners on unmount so a mid-drag close can't
  // leak pointermove/pointerup handlers on window. The refs hold the exact
  // functions that were added (stored in startDrag); if no drag is active
  // they are null and removeEventListener is a no-op on a missing handler.
  useEffect(() => {
    return () => {
      if (pointerMoveRef.current) window.removeEventListener("pointermove", pointerMoveRef.current);
      if (pointerUpRef.current) window.removeEventListener("pointerup", pointerUpRef.current);
    };
  }, []);
  function handlePointerMove(e: PointerEvent) {
    if (dragStartYRef.current === null) return;
    const delta = e.clientY - dragStartYRef.current;
    // Dragging down from full toward peek, or up from peek toward full.
    if (dragStartGeometryRef.current === "full" && delta > 50) setGeometry("peek");
    else if (dragStartGeometryRef.current === "peek" && delta < -50) setGeometry("full");
  }

  function handlePointerUp(e: PointerEvent) {
    if (dragStartYRef.current === null) return;
    const delta = e.clientY - dragStartYRef.current;
    const wasDrag = Math.abs(delta) > 10;
    dragStartYRef.current = null;
    if (!wasDrag) {
      // Tap toggles between the two geometries.
      setGeometry((g) => (g === "peek" ? "full" : "peek"));
    }
    window.removeEventListener("pointermove", handlePointerMove);
    window.removeEventListener("pointerup", handlePointerUp);
    pointerMoveRef.current = null;
    pointerUpRef.current = null;
  }

  function startDrag(e: ReactPointerEvent) {
    dragStartYRef.current = e.clientY;
    dragStartGeometryRef.current = geometry;
    // Store the listener functions in the refs BEFORE adding them so the
    // unmount cleanup can remove these exact references.
    pointerMoveRef.current = handlePointerMove;
    pointerUpRef.current = handlePointerUp;
    window.addEventListener("pointermove", handlePointerMove);
    window.addEventListener("pointerup", handlePointerUp);
  }

  const composedPanelClassName = panelClassName
    ? `${SIDE_CLASS[side]} ${SIZE_CLASS[size]} ${panelClassName}`
    : `${SIDE_CLASS[side]} ${SIZE_CLASS[size]}`;
  const composedExpandablePanelClassName = panelClassName ? `${SIDE_CLASS[side]} ${panelClassName}` : SIDE_CLASS[side];

  if (!expandable) {
    return (
      <OverlayPanel
        open={open}
        onClose={onClose}
        title={title}
        footer={footer}
        panelClassName={composedPanelClassName}
        bodyClassName={bodyClassName}
      >
        {children}
      </OverlayPanel>
    );
  }

  const heightStyle: CSSProperties =
    geometry === "full" ? { height: "100vh" } : { height: `${expandable.peekHeight}px` };

  return (
    <OverlayPanel
      open={open}
      onClose={onClose}
      title={title}
      footer={footer}
      handle={<DragHandle onPointerDown={startDrag} />}
      headerClassName={EXPANDABLE_HEADER_CLASS}
      bodyClassName={bodyClassName ? `${EXPANDABLE_BODY_CLASS} ${bodyClassName}` : EXPANDABLE_BODY_CLASS}
      panelClassName={`${composedExpandablePanelClassName} ${EXPANDABLE_BOTTOM_CLASS}`}
      style={heightStyle}
    >
      <div data-geometry={geometry}>{children}</div>
    </OverlayPanel>
  );
}
