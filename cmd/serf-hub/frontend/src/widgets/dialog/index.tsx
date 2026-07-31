import type { ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./dialog.module.css";
import { OverlayPanel } from "./OverlayPanel";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** "default" (the default) is the compact, content-sized dialog every
   * other caller wants. "large" fills almost the whole viewport instead of
   * capping at a fixed width - for lightbox-style content (ImageGallery's
   * image lightbox, kata b4xf), where the content itself, not a settings
   * form or a confirmation prompt, is the point. */
  size?: "default" | "large";
}

const BASE_PANEL_CLASS = requireClass(styles.panel, "dialog.module.css", "panel");
const SIZE_CLASS: Record<NonNullable<DialogProps["size"]>, string> = {
  default: `${BASE_PANEL_CLASS} ${requireClass(styles.dialogVariant, "dialog.module.css", "dialogVariant")}`,
  large: `${BASE_PANEL_CLASS} ${requireClass(styles.dialogVariantLarge, "dialog.module.css", "dialogVariantLarge")}`,
};

/**
 * Modal dialog: centered, 120ms fade-scale on open, Escape and scrim-click
 * both close it, focus is trapped inside while open and restored to
 * whatever triggered it on close. Sheet (../sheet) shares this exact
 * contract via OverlayPanel, swapping only the panel's geometry/animation;
 * `size` (above) is a second, narrower axis of the same kind - still the
 * centered fade-scale dialog, just a different cap on how big it's allowed
 * to grow.
 */
export function Dialog({ open, onClose, title, children, footer, size = "default" }: DialogProps) {
  return (
    <OverlayPanel open={open} onClose={onClose} title={title} footer={footer} panelClassName={SIZE_CLASS[size]}>
      {children}
    </OverlayPanel>
  );
}
