import { type ReactNode, useRef } from "react";
import dialogStyles from "../dialog/dialog.module.css";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";
import { useSheetSwipe } from "./useSheetSwipe";

export type SheetSide = "right" | "bottom" | "left";
export type SheetSize = "standard" | "wide";

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

const HANDLE_BAR_CLASS = requireClass(styles.handleBar, "sheet.module.css", "handleBar");
const GRAB_HANDLE_CLASS = requireClass(styles.grabHandle, "sheet.module.css", "grabHandle");
const BOTTOM_BODY_CLASS = requireClass(styles.bottomBody, "sheet.module.css", "bottomBody");
const BOTTOM_HEADER_CLASS = requireClass(styles.bottomHeader, "sheet.module.css", "bottomHeader");

/**
 * Slide-over panel anchored to the right edge, the left edge, or the
 * bottom edge - otherwise the exact Dialog contract (see ../dialog),
 * sharing its OverlayPanel: scrim, Escape/scrim-click to close, trapped
 * and restored focus, close button. Only the panel's own geometry and its
 * motion differ: it slides in from its edge and back out to it
 * (sheet.module.css, the Slide recipe).
 *
 * `side="bottom"` additionally supports swipe-down dismissal from the grab
 * pill, header, or unscrolled body (and renders the coarse-pointer grab pill);
 * left and right sheets do not swipe.
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
  panelClassName,
  bodyClassName,
}: SheetProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrimRef = useRef<HTMLDivElement>(null);

  const { onHandlePointerDown, onHeaderPointerDown } = useSheetSwipe({
    open,
    side,
    onClose,
    panelRef,
    bodyRef,
    scrimRef,
  });

  const composedPanelClassName = panelClassName
    ? `${SIDE_CLASS[side]} ${SIZE_CLASS[size]} ${panelClassName}`
    : `${SIDE_CLASS[side]} ${SIZE_CLASS[size]}`;
  const bottomBodyClassName = side === "bottom" ? BOTTOM_BODY_CLASS : undefined;
  const bottomHeaderClassName = side === "bottom" ? BOTTOM_HEADER_CLASS : undefined;
  const composedBodyClassName = [bottomBodyClassName, bodyClassName].filter(Boolean).join(" ");
  const bottomHeaderProps = side === "bottom" ? { onPointerDown: onHeaderPointerDown } : undefined;

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
          <div
            className={GRAB_HANDLE_CLASS}
            data-testid="sheet-grab-pill"
            aria-hidden
            onPointerDown={onHandlePointerDown}
          >
            <div className={HANDLE_BAR_CLASS} />
          </div>
        ) : undefined
      }
      headerClassName={bottomHeaderClassName}
      bodyClassName={composedBodyClassName}
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
