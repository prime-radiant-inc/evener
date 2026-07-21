import type { ReactNode } from "react";
import dialogStyles from "../dialog/dialog.module.css";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";

export type SheetSide = "right" | "bottom";

export interface SheetProps {
  side?: SheetSide;
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}

const BASE_PANEL_CLASS = requireClass(dialogStyles.panel, "dialog.module.css", "panel");

const SIDE_CLASS: Record<SheetSide, string> = {
  right: `${BASE_PANEL_CLASS} ${requireClass(styles.right, "sheet.module.css", "right")}`,
  bottom: `${BASE_PANEL_CLASS} ${requireClass(styles.bottom, "sheet.module.css", "bottom")}`,
};

/**
 * Slide-over panel anchored to the right edge or the bottom edge -
 * otherwise the exact Dialog contract (see ../dialog), sharing its
 * OverlayPanel: scrim, Escape/scrim-click to close, trapped and restored
 * focus, close button. Only the panel's own geometry and slide-in
 * animation differ (sheet.module.css).
 */
export function Sheet({ side = "right", open, onClose, title, children, footer }: SheetProps) {
  return (
    <OverlayPanel open={open} onClose={onClose} title={title} footer={footer} panelClassName={SIDE_CLASS[side]}>
      {children}
    </OverlayPanel>
  );
}
