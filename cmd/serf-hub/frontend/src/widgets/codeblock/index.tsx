import { useEffect, useMemo, useState } from "react";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import styles from "./codeblock.module.css";

export interface CodeBlockProps {
  text: string;
  language?: string;
  /** Shows a right-aligned line-number column ahead of each line. */
  showLineNumbers?: boolean;
  /** The copy control's accessible name. Defaults to "Copy"; a caller whose
   * block holds something more specific (a tool's output) should say so. */
  copyLabel?: string;
}

const CLASS = {
  root: requireClass(styles.root, "codeblock.module.css", "root"),
  header: requireClass(styles.header, "codeblock.module.css", "header"),
  language: requireClass(styles.language, "codeblock.module.css", "language"),
  copy: requireClass(styles.copy, "codeblock.module.css", "copy"),
  pre: requireClass(styles.pre, "codeblock.module.css", "pre"),
  code: requireClass(styles.code, "codeblock.module.css", "code"),
  line: requireClass(styles.line, "codeblock.module.css", "line"),
  gutter: requireClass(styles.gutter, "codeblock.module.css", "gutter"),
};

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

/**
 * A block of source/tool-output text: mono font, an optional language label
 * and line-number gutter, and a copy control inset into the block's top-right
 * corner. No syntax highlighting - YAGNI this wave; add a highlighter only when
 * a real consumer needs one.
 *
 * Long lines WRAP; the block never scrolls horizontally. Tool output is prose-
 * shaped far more often than it is table-shaped, and a horizontal scroller hides
 * the ends of exactly the lines a reader opened the block for. DiffBlock keeps
 * its own scroller for the opposite reason - column alignment across lines is
 * part of a diff's meaning, and wrapping destroys it.
 */
export function CodeBlock({ text, language, showLineNumbers = false, copyLabel = "Copy" }: CodeBlockProps) {
  const lines = useMemo(() => (showLineNumbers ? text.split("\n") : null), [text, showLineNumbers]);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => clearTimeout(timer);
  }, [copied]);

  async function handleCopy() {
    // Clipboard access requires a secure context and isn't implemented by
    // every test/embed environment - degrade to a no-op rather than throw.
    if (!navigator.clipboard?.writeText) return;
    await navigator.clipboard.writeText(text);
    setCopied(true);
  }

  return (
    <div className={CLASS.root}>
      {/* The copy control is inset into the block's own top-right corner rather
          than sitting in a full-width labelled row of its own: the block's
          content is the point, and a header band spent on one word doubles the
          block's visual weight (A4). */}
      <div className={CLASS.header}>
        {language !== undefined && <span className={CLASS.language}>{language}</span>}
        <span className={CLASS.copy}>
          <IconButton
            label={copied ? "Copied" : copyLabel}
            icon={copied ? <CopiedIcon /> : <CopyIcon />}
            variant="quiet"
            size="xs"
            onClick={handleCopy}
          />
        </span>
      </div>
      <pre className={CLASS.pre}>
        <code className={CLASS.code}>
          {lines
            ? lines.map((line, i) => (
                // i is this line's actual displayed line number (the
                // gutter below), not an arbitrary position - lines is
                // derived fresh from the text prop every render, in the
                // same fixed order text.split gives it.
                // biome-ignore lint/suspicious/noArrayIndexKey: i is the displayed line number itself, see above
                <span key={i} className={CLASS.line}>
                  <span className={CLASS.gutter} aria-hidden="true">
                    {i + 1}
                  </span>
                  <span>{line}</span>
                </span>
              ))
            : text}
        </code>
      </pre>
    </div>
  );
}
