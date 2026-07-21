import { useEffect, useMemo, useState } from "react";
import { Button } from "../button";
import { requireClass } from "../internal/requireClass";
import styles from "./codeblock.module.css";

export interface CodeBlockProps {
  text: string;
  language?: string;
  /** Shows a right-aligned line-number column ahead of each line. */
  showLineNumbers?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "codeblock.module.css", "root"),
  header: requireClass(styles.header, "codeblock.module.css", "header"),
  language: requireClass(styles.language, "codeblock.module.css", "language"),
  copyWrapper: requireClass(styles.copyWrapper, "codeblock.module.css", "copyWrapper"),
  pre: requireClass(styles.pre, "codeblock.module.css", "pre"),
  code: requireClass(styles.code, "codeblock.module.css", "code"),
  line: requireClass(styles.line, "codeblock.module.css", "line"),
  gutter: requireClass(styles.gutter, "codeblock.module.css", "gutter"),
};

const COPIED_RESET_MS = 2_000;

/**
 * A block of source/tool-output text: mono font, an optional language label
 * and line-number gutter, and a copy button. No syntax highlighting - YAGNI
 * this wave; add a highlighter only when a real consumer needs one.
 */
export function CodeBlock({ text, language, showLineNumbers = false }: CodeBlockProps) {
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
      <div className={CLASS.header}>
        {language !== undefined && <span className={CLASS.language}>{language}</span>}
        <span className={CLASS.copyWrapper}>
          <Button variant="quiet" size="sm" onClick={handleCopy}>
            {copied ? "Copied" : "Copy"}
          </Button>
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
