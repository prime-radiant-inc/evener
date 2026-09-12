// CopyButton is the standard copy-to-clipboard control: an IconButton with
// a copy glyph, "Copied" feedback with a timed reset, and a clipboard guard
// for test/embed environments without navigator.clipboard. It replaces the
// inline copy logic previously duplicated in CodeBlock, jobTools.tsx's
// CopyTextButton, and DelegateStatusBody's CopyRawButton.
//
// The label prop is the button's accessible name BEFORE copying; while the
// "Copied" state is active, it becomes "Copied" automatically. Pass a
// specific label ("Copy output", "Copy raw JSON") so screen readers
// announce what the button copies.
import { useEffect, useState } from "react";
import type { IconButtonProps } from "../iconbutton";
import { IconButton } from "../iconbutton";

const COPIED_RESET_MS = 2_000;

function CopyIcon() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <rect x="4.5" y="1.5" width="8" height="8" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M9.5 12.5H3A1.5 1.5 0 0 1 1.5 11V4.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}

function CopiedIcon() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <path d="M2 7.5 L5.5 11 L12 3.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

export interface CopyButtonProps extends Omit<IconButtonProps, "label" | "icon" | "onClick"> {
  /** Text written to the clipboard on click. */
  text: string;
  /** The button's accessible name before copying. While the "Copied" state
   * is active, it becomes "Copied" automatically. Defaults to "Copy". */
  label?: string;
  /** IconButton variant; defaults to "quiet". */
  variant?: IconButtonProps["variant"];
  /** IconButton size; defaults to "xs". */
  size?: IconButtonProps["size"];
}

export function CopyButton({ text, label = "Copy", variant = "quiet", size = "xs", ...rest }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => clearTimeout(timer);
  }, [copied]);

  return (
    <IconButton
      {...rest}
      label={copied ? "Copied" : label}
      icon={copied ? <CopiedIcon /> : <CopyIcon />}
      variant={variant}
      size={size}
      onClick={() => {
        // Clipboard access requires a secure context and isn't implemented by
        // every test/embed environment — degrade to a no-op rather than throw.
        if (!navigator.clipboard?.writeText) return;
        void navigator.clipboard.writeText(text).then(
          () => setCopied(true),
          () => {}, // swallow rejection — a failed copy is a no-op, not an error
        );
      }}
    />
  );
}
