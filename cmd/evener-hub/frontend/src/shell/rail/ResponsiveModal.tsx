import type { ReactNode } from "react";
import { Dialog, Sheet, type SheetProps } from "../../widgets";
import { useIsMobile } from "../useIsMobile";

export interface ResponsiveModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  /** The Sheet's footer; the Dialog shows it too unless showDialogFooter is false. */
  footer?: ReactNode;
  /** The caller's own layout answer, for a surface that follows stored layout
   * state rather than the live viewport. Defaults to `useIsMobile`. */
  mobile?: boolean;
  /** Sheet geometry, for a caller that needs the wide sheet. */
  sheetSize?: SheetProps["size"];
  /** False when the caller puts its actions in the dialog body instead. */
  showDialogFooter?: boolean;
}

/**
 * One modal whose surface follows the layout: the bottom Sheet on a phone,
 * the centered Dialog everywhere else. Both are the same OverlayPanel
 * contract (scrim, Escape, focus trap/restore).
 */
export function ResponsiveModal({
  open,
  onClose,
  title,
  children,
  footer,
  mobile,
  sheetSize,
  showDialogFooter = true,
}: ResponsiveModalProps) {
  const viewportMobile = useIsMobile();
  if (mobile ?? viewportMobile) {
    return (
      <Sheet open={open} side="bottom" size={sheetSize} onClose={onClose} title={title} footer={footer}>
        {children}
      </Sheet>
    );
  }
  return (
    <Dialog open={open} onClose={onClose} title={title} footer={showDialogFooter ? footer : undefined}>
      {children}
    </Dialog>
  );
}
