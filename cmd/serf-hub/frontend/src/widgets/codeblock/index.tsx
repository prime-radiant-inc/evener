import { Fragment, useEffect, useMemo, useState } from "react";
import { Button } from "../button";
import { IconButton } from "../iconbutton";
import { requireClass } from "../internal/requireClass";
import { parseAnsiLines } from "./ansi";
import { AnsiLineContent } from "./ansiLine";
import styles from "./codeblock.module.css";

export interface CodeBlockProps {
  text: string;
  /** Text written by the copy control when display preparation changed the
   * rendered source. Defaults to text. */
  copyText?: string;
  language?: string;
  /** Interpret ANSI Select Graphic Rendition sequences as styled text. Other
   * terminal controls are consumed; this remains a log block, not a terminal. */
  ansi?: boolean;
  /** Shows a right-aligned line-number column ahead of each line. */
  showLineNumbers?: boolean;
  /** The copy control's accessible name. Defaults to "Copy"; a caller whose
   * block holds something more specific (a tool's output) should say so. */
  copyLabel?: string;
}

const CLASS = {
  root: requireClass(styles.root, "codeblock.module.css", "root"),
  codeArea: requireClass(styles.codeArea, "codeblock.module.css", "codeArea"),
  header: requireClass(styles.header, "codeblock.module.css", "header"),
  language: requireClass(styles.language, "codeblock.module.css", "language"),
  copy: requireClass(styles.copy, "codeblock.module.css", "copy"),
  pre: requireClass(styles.pre, "codeblock.module.css", "pre"),
  code: requireClass(styles.code, "codeblock.module.css", "code"),
  line: requireClass(styles.line, "codeblock.module.css", "line"),
  gutter: requireClass(styles.gutter, "codeblock.module.css", "gutter"),
  fold: requireClass(styles.fold, "codeblock.module.css", "fold"),
};

const COPIED_RESET_MS = 2_000;

// 67zh: a raw tool-output block (a pytest traceback, a shell dump) with no
// cap fills an entire narrow viewport, forcing a reader to scroll THROUGH it
// rather than past it. Past TAIL_VISIBLE_LINES lines, the block folds to its
// tail by default - mirroring this file's own tools/helpers.ts's tailFold,
// used project-wide for the exact same content (settled shell/read output):
// keep the tail, elide the head, and say honestly how much was elided. The
// tail, not the head, is kept because the informative part of a long dump
// (pytest's FAILURES section, a command's final error) is almost always at
// the end - a fold that hid the tail instead would hide the one thing a
// reader who scrolled this far actually came for.
const TAIL_VISIBLE_LINES = 14;

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
export function CodeBlock({
  text,
  copyText,
  language,
  ansi = false,
  showLineNumbers = false,
  copyLabel = "Copy",
}: CodeBlockProps) {
  const allLines = useMemo(() => (ansi ? parseAnsiLines(text) : text.split("\n")), [ansi, text]);
  const isLong = allLines.length > TAIL_VISIBLE_LINES;
  // "revealed" lifts the fold - once the reader has asked to see everything,
  // re-folding is their own explicit "Show fewer lines" click below, not
  // something a re-render should silently undo out from under them.
  const [revealed, setRevealed] = useState(false);
  const folded = isLong && !revealed;
  // The real line number the tail STARTS at (1-based) - needed so a folded
  // gutter reads e.g. "15, 16, 17…", the lines' actual position in the
  // output, never renumbered from 1 as if the head had never existed.
  const tailStart = folded ? allLines.length - TAIL_VISIBLE_LINES : 0;
  const visibleLines = folded ? allLines.slice(tailStart) : allLines;
  const hiddenCount = tailStart;

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
    // Always the FULL text, never `visibleLines` - folding is a display
    // convenience, not a truncation, and copy must give back exactly what
    // the tool actually produced.
    await navigator.clipboard.writeText(copyText ?? text);
    setCopied(true);
  }

  return (
    <div className={CLASS.root}>
      {/* The banner sits ABOVE the tail it's folding away: the hidden lines
          would render here if revealed, so this is where "reveal them" belongs -
          not a footer, which would put the control past the content it's about. */}
      {folded && (
        <div className={CLASS.fold}>
          <Button variant="quiet" size="sm" onClick={() => setRevealed(true)}>
            Show {hiddenCount} earlier line{hiddenCount === 1 ? "" : "s"}
          </Button>
        </div>
      )}
      <div className={CLASS.codeArea}>
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
            {showLineNumbers
              ? visibleLines.map((line, i) => (
                  // tailStart + i is this line's actual displayed line number
                  // (the gutter below), not an arbitrary position - visibleLines
                  // is derived fresh from the text prop every render, in the
                  // same fixed order text.split gives it.
                  // biome-ignore lint/suspicious/noArrayIndexKey: tailStart + i is the displayed line number itself, see above
                  <span key={i} className={CLASS.line}>
                    <span className={CLASS.gutter} aria-hidden="true">
                      {tailStart + i + 1}
                    </span>
                    <span>{typeof line === "string" ? line : <AnsiLineContent line={line} />}</span>
                  </span>
                ))
              : ansi
                ? visibleLines.map((line, index) => (
                    // biome-ignore lint/suspicious/noArrayIndexKey: tailStart + index is the stable displayed source line number
                    <Fragment key={tailStart + index}>
                      {index > 0 ? "\n" : null}
                      {typeof line === "string" ? line : <AnsiLineContent line={line} />}
                    </Fragment>
                  ))
                : visibleLines.join("\n")}
          </code>
        </pre>
      </div>
      {/* isLong (not just `folded`) - once revealed, the way back to the tail
          view has to keep showing even though `folded` itself just went false. */}
      {isLong && revealed && (
        <div className={CLASS.fold}>
          <Button variant="quiet" size="sm" onClick={() => setRevealed(false)}>
            Show fewer lines
          </Button>
        </div>
      )}
    </div>
  );
}
