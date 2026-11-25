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
import { useSheetSwipe } from "./useSheetSwipe";

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
  /** Actions beside the title, before the sheet's Close control. */
  headerActions?: ReactNode;
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
const GRAB_HANDLE_CLASS = requireClass(styles.grabHandle, "sheet.module.css", "grabHandle");
const BOTTOM_BODY_CLASS = requireClass(styles.bottomBody, "sheet.module.css", "bottomBody");
const BOTTOM_HEADER_CLASS = requireClass(styles.bottomHeader, "sheet.module.css", "bottomHeader");
const EXPANDABLE_BOTTOM_CLASS = requireClass(styles.expandableBottom, "sheet.module.css", "expandableBottom");
const EXPANDABLE_HEADER_CLASS = requireClass(styles.expandableHeader, "sheet.module.css", "expandableHeader");
const EXPANDABLE_BODY_CLASS = requireClass(styles.expandableBody, "sheet.module.css", "expandableBody");

function SheetHandle({
  className,
  testId,
  ariaHidden,
  onPointerDown,
}: {
  className: string;
  testId: string;
  ariaHidden?: true;
  onPointerDown: (e: ReactPointerEvent) => void;
}) {
  return (
    <div className={className} data-testid={testId} aria-hidden={ariaHidden} onPointerDown={onPointerDown}>
      <div className={HANDLE_BAR_CLASS} />
    </div>
  );
}

/**
 * Slide-over panel anchored to the right edge, the left edge, or the
 * bottom edge - otherwise the exact Dialog contract (see ../dialog),
 * sharing its OverlayPanel: scrim, Escape/scrim-click to close, trapped
 * and restored focus, close button. Only the panel's own geometry and its
 * motion differ: it slides in from its edge and back out to it
 * (sheet.module.css, the Slide recipe).
 *
 * When `expandable` is set, the Sheet additionally renders a drag handle
 * above the header and manages a "peek" | "full" geometry state, resetting
 * to "full" on every open false->true transition (when fullScreenFirst).
 * The handle drags between the two geometries (pointermove past a threshold
 * on its own window) and taps to toggle. The current geometry is surfaced on a
 * wrapper div inside the body as a `data-geometry` attribute for tests and
 * for CSS to key off, and dragging down from "peek" dismisses via `onClose`.
 * When `expandable` is not set, this is the plain Sheet - fixed geometry,
 * with `side="bottom"` additionally supporting swipe-down dismissal from the
 * grab pill, header, or unscrolled body (and rendering the coarse-pointer grab
 * pill); left and right sheets render and behave exactly as before.
 */
export function Sheet({
  side = "right",
  size = "standard",
  open,
  onClose,
  title,
  headerActions,
  children,
  footer,
  expandable,
  panelClassName,
  bodyClassName,
}: SheetProps) {
  const [geometry, setGeometry] = useState<"peek" | "full">(expandable?.fullScreenFirst ? "full" : "peek");
  const panelRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrimRef = useRef<HTMLDivElement>(null);

  // Reset geometry to full on every open transition (false->true). An effect
  // keyed on `open` (and fullScreenFirst) is the only place that observes
  // that transition; the render-time useState initializer only fires once,
  // on mount, so without this a peek reached before close would survive a
  // reopen.
  useEffect(() => {
    if (open && expandable?.fullScreenFirst) setGeometry("full");
  }, [open, expandable?.fullScreenFirst]);

  const { onHandlePointerDown, onHeaderPointerDown } = useSheetSwipe({
    open,
    side,
    onClose,
    panelRef,
    bodyRef,
    scrimRef,
    expandable: expandable ? { geometry, onGeometryChange: setGeometry } : undefined,
  });

  const composedPanelClassName = panelClassName
    ? `${SIDE_CLASS[side]} ${SIZE_CLASS[size]} ${panelClassName}`
    : `${SIDE_CLASS[side]} ${SIZE_CLASS[size]}`;
  const composedExpandablePanelClassName = panelClassName ? `${SIDE_CLASS[side]} ${panelClassName}` : SIDE_CLASS[side];
  const bottomBodyClassName = side === "bottom" ? BOTTOM_BODY_CLASS : undefined;
  const bottomHeaderClassName = side === "bottom" ? BOTTOM_HEADER_CLASS : undefined;
  const plainBodyClassName = [bottomBodyClassName, bodyClassName].filter(Boolean).join(" ");
  const expandableBodyClassName = [EXPANDABLE_BODY_CLASS, bottomBodyClassName, bodyClassName].filter(Boolean).join(" ");
  const expandableHeaderClassName = [EXPANDABLE_HEADER_CLASS, bottomHeaderClassName].filter(Boolean).join(" ");
  const bottomHeaderProps = side === "bottom" ? { onPointerDown: onHeaderPointerDown } : undefined;

  if (!expandable) {
    return (
      <OverlayPanel
        open={open}
        onClose={onClose}
        title={title}
        headerActions={headerActions}
        footer={footer}
        panelClassName={composedPanelClassName}
        handle={
          side === "bottom" ? (
            <SheetHandle
              className={GRAB_HANDLE_CLASS}
              testId="sheet-grab-pill"
              ariaHidden
              onPointerDown={onHandlePointerDown}
            />
          ) : undefined
        }
        headerClassName={bottomHeaderClassName}
        bodyClassName={plainBodyClassName}
        motion="slide"
        panelRef={panelRef}
        bodyRef={bodyRef}
        scrimRef={scrimRef}
        headerProps={bottomHeaderProps}
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
      headerActions={headerActions}
      footer={footer}
      handle={<SheetHandle className={HANDLE_CLASS} testId="sheet-handle" onPointerDown={onHandlePointerDown} />}
      headerClassName={expandableHeaderClassName}
      bodyClassName={expandableBodyClassName}
      panelClassName={`${composedExpandablePanelClassName} ${EXPANDABLE_BOTTOM_CLASS}`}
      style={heightStyle}
      motion="slide"
      panelRef={panelRef}
      bodyRef={bodyRef}
      scrimRef={scrimRef}
      headerProps={bottomHeaderProps}
    >
      <div data-geometry={geometry}>{children}</div>
    </OverlayPanel>
  );
}
