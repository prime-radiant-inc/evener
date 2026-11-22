// CopyButton is the standard copy-to-clipboard control. By default it is an
// IconButton with a copy glyph and "Copied" feedback with a timed reset; the
// "text" presentation is a labelled Button whose label carries the
// confirmation, for copy actions that stand as visible text actions. Both
// presentations use clipboard.ts's Clipboard API + execCommand fallback. It
// replaces the inline copy logic previously duplicated in CodeBlock,
// jobTools.tsx's CopyTextButton, DelegateStatusBody's CopyRawButton, and the
// credentials device-code dialog.
//
// The label prop is the button's accessible name BEFORE copying; while the
// "Copied" state is active, it becomes "Copied" automatically. Pass a
// specific label ("Copy output", "Copy raw JSON") so screen readers
// announce what the button copies.
import { useEffect, useState } from "react";
import { Button } from "../button";
import type { IconButtonProps } from "../iconbutton";
import { IconButton } from "../iconbutton";
import { copyText } from "./clipboard";

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
  /** "icon" (default) is the icon-only IconButton; "text" is a labelled
   * Button, for a copy action whose label carries the confirmation. */
  presentation?: "icon" | "text";
  /** Called after each copy attempt settles: true when the text reached the
   * clipboard, false when the Clipboard API and the execCommand fallback both
   * failed. The text presentation's callers use it to report a failed copy
   * and to enable a follow-up action. */
  onResult?: (ok: boolean) => void;
}

export function CopyButton({
  text,
  label = "Copy",
  variant = "quiet",
  size,
  presentation = "icon",
  onResult,
  ...rest
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    // The text presentation keeps "Copied ✓" in place of the action label,
    // like the device-code button it replaced, until it unmounts; the icon
    // presentation's glyph is the one that resets.
    if (!copied || presentation === "text") return;
    const timer = setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => clearTimeout(timer);
  }, [copied, presentation]);

  async function handleCopy(doc: Document): Promise<void> {
    const ok = await copyText(text, doc);
    if (ok) setCopied(true);
    onResult?.(ok);
  }

  if (presentation === "text") {
    return (
      <Button
        {...rest}
        variant={variant}
        size={size ?? "md"}
        onClick={(event) => void handleCopy(event.currentTarget.ownerDocument)}
      >
        {copied ? "Copied ✓" : label}
      </Button>
    );
  }

  return (
    <IconButton
      {...rest}
      label={copied ? "Copied" : label}
      icon={copied ? <CopiedIcon /> : <CopyIcon />}
      variant={variant}
      size={size ?? "xs"}
      onClick={(event) => void handleCopy(event.currentTarget.ownerDocument)}
    />
  );
}
