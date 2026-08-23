import type { JSX } from "react";

export interface PlainTextBlockProps {
  /**
   * Untrusted plain text (tool output, filenames, notices, errors, user text).
   * Rendered as an escaped React text node — never via `dangerouslySetInnerHTML`.
   */
  readonly text: string;
  readonly className?: string;
}

/**
 * Renders untrusted text as escaped plain text. React escapes all string
 * children by default, so HTML characters like `<`, `>`, and `&` are displayed
 * literally and cannot execute. This component never accepts a
 * `TrustedHTMLString` — only a plain `string`.
 */
export function PlainTextBlock({
  text,
  className,
}: PlainTextBlockProps): JSX.Element {
  return (
    <div
      className={`evener-plaintext-block${className ? ` ${className}` : ""}`}
    >
      {text}
    </div>
  );
}
