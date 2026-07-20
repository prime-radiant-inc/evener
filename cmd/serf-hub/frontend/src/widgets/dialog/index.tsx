import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import { OverlayPanel } from "./OverlayPanel";
import styles from "./dialog.module.css";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}

const PANEL_CLASS = [
  requireClass(styles.panel, "dialog.module.css", "panel"),
  requireClass(styles.dialogVariant, "dialog.module.css", "dialogVariant"),
].join(" ");

/**
 * Modal dialog: centered, 120ms fade-scale on open, Escape and scrim-click
 * both close it, focus is trapped inside while open and restored to
 * whatever triggered it on close. Sheet (../sheet) shares this exact
 * contract via OverlayPanel, swapping only the panel's geometry/animation.
 */
export function Dialog({ open, onClose, title, children, footer }: DialogProps) {
  return (
    <OverlayPanel open={open} onClose={onClose} title={title} footer={footer} panelClassName={PANEL_CLASS}>
      {children}
    </OverlayPanel>
  );
}
