import {
  type JSX,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import { createPortal } from "react-dom";
import { IconButton } from "./IconButton";

export interface SheetProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly title: string;
  readonly children: ReactNode;
  /** When true, Escape is captured (default true). Back-button JS calls onClose. */
  readonly dismissOnEscape?: boolean;
}

/**
 * Detent bottom sheet in a portal with a focus trap, Escape dismissal, and a
 * deterministic upward motion (removed under reduced motion). One sheet at a
 * time — never stacked. The grabber + Done button give explicit close affordances.
 */
export function Sheet({
  open,
  onClose,
  title,
  children,
  dismissOnEscape = true,
}: SheetProps): JSX.Element | null {
  const dialogRef = useRef<HTMLDivElement>(null);
  const previouslyFocused = useRef<HTMLElement | null>(null);

  // Capture the element that had focus before opening; restore on close.
  useLayoutEffect(() => {
    if (!open) return;
    previouslyFocused.current =
      (document.activeElement as HTMLElement | null) ?? null;
    return () => {
      previouslyFocused.current?.focus?.();
    };
  }, [open]);

  // Focus the first focusable element when the sheet opens.
  useEffect(() => {
    if (!open) return;
    const dialog = dialogRef.current;
    if (dialog === null) return;
    // Defer one frame so portal content is painted.
    const id = window.requestAnimationFrame(() => {
      const focusable = queryFocusable(dialog);
      focusable[0]?.focus();
    });
    return () => window.cancelAnimationFrame(id);
  }, [open]);

  // Document-level Escape handler so the key reaches the sheet regardless of
  // which portal child currently holds focus.
  useEffect(() => {
    if (!open || !dismissOnEscape) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, dismissOnEscape, onClose]);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Tab") {
      trapTab(event, dialogRef.current);
    }
  }, []);

  if (!open) return null;

  return createPortal(
    <>
      <div
        className="evener-sheet-overlay"
        onClick={onClose}
        aria-hidden="true"
      />
      <div
        ref={dialogRef}
        className="evener-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={handleKeyDown}
      >
        <div className="evener-sheet__grabber" aria-hidden="true" />
        <div className="evener-sheet__header">
          <span className="evener-sheet__title">{title}</span>
          <IconButton aria-label="Done" onClick={onClose}>
            Done
          </IconButton>
        </div>
        <div className="evener-sheet__body">{children}</div>
      </div>
    </>,
    document.body,
  );
}

function queryFocusable(root: HTMLElement): HTMLElement[] {
  const selector =
    'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
  // jsdom reports offsetParent as null even for visible elements; we only
  // need the set of tabbable elements for focus management, not geometry.
  return [...root.querySelectorAll<HTMLElement>(selector)];
}

function trapTab(
  event: KeyboardEvent<HTMLDivElement>,
  dialog: HTMLElement | null,
): void {
  if (dialog === null) return;
  const focusable = queryFocusable(dialog);
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (first === undefined || last === undefined) return;
  const active = document.activeElement as HTMLElement | null;
  if (event.shiftKey) {
    if (active === first || active === null) {
      event.preventDefault();
      last.focus();
    }
  } else {
    if (active === last) {
      event.preventDefault();
      first.focus();
    }
  }
}
