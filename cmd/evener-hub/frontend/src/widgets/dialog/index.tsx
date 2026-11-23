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
  /** Extra class appended to the panel, after the size variant. */
  panelClassName?: string;
  /** Extra class appended to the body, after the shared body class. */
  bodyClassName?: string;
  /** "none" drops the float motion and the size geometry, for a caller that
   * owns the panel's geometry and mounts it outright (DirectoryPicker).
   * Default "float". */
  motion?: "float" | "none";
}

const BASE_PANEL_CLASS = requireClass(styles.panel, "dialog.module.css", "panel");
const SIZE_CLASS: Record<NonNullable<DialogProps["size"]>, string> = {
  default: requireClass(styles.dialogVariant, "dialog.module.css", "dialogVariant"),
  large: requireClass(styles.dialogVariantLarge, "dialog.module.css", "dialogVariantLarge"),
};

/**
 * Modal dialog: centered, floating in and out (the Float recipe), Escape and scrim-click
 * both close it, focus is trapped inside while open and restored to
 * whatever triggered it on close. Sheet (../sheet) shares this exact
 * contract via OverlayPanel, swapping only the panel's geometry and motion;
 * `size` (above) is a second, narrower axis of the same kind - still the
 * centered floating dialog, just a different cap on how big it's allowed
 * to grow.
 */
export function Dialog(props: DialogProps) {
  const { size = "default", panelClassName, motion = "float", ...rest } = props;
  const panel = `${BASE_PANEL_CLASS}${motion === "float" ? ` ${SIZE_CLASS[size]}` : ""}${panelClassName ? ` ${panelClassName}` : ""}`;
  return <OverlayPanel {...rest} panelClassName={panel} motion={motion} />;
}
