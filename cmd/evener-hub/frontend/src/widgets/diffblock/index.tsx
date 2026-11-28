import { useMemo } from "react";
import { CodeBlockHeader } from "../codeblock";
import { CopyButton } from "../copybutton";
import { requireClass } from "../internal/requireClass";
import styles from "./diffblock.module.css";

export interface DiffBlockProps {
  unified: string;
}

type DiffLineKind = "header" | "add" | "del" | "context" | "meta";

interface DiffLine {
  kind: DiffLineKind;
  marker: string;
  content: string;
}

const CLASS = {
  root: requireClass(styles.root, "diffblock.module.css", "root"),
  line: requireClass(styles.line, "diffblock.module.css", "line"),
  marker: requireClass(styles.marker, "diffblock.module.css", "marker"),
  content: requireClass(styles.content, "diffblock.module.css", "content"),
  add: requireClass(styles.add, "diffblock.module.css", "add"),
  del: requireClass(styles.del, "diffblock.module.css", "del"),
  header: requireClass(styles.header, "diffblock.module.css", "header"),
  context: requireClass(styles.context, "diffblock.module.css", "context"),
  meta: requireClass(styles.meta, "diffblock.module.css", "meta"),
};

const KIND_CLASS: Record<DiffLineKind, string> = {
  header: CLASS.header,
  add: CLASS.add,
  del: CLASS.del,
  context: CLASS.context,
  meta: CLASS.meta,
};

// Git's exact, unlocalized wording for "this line has no trailing
// newline in the file" - not a context line (it describes the diff, it
// isn't part of either file's content), so it gets its own muted tone
// rather than reading as an unchanged line of code.
const NO_NEWLINE_MARKER = "\\ No newline at end of file";

/**
 * Classifies each line of unified-diff text. "---"/"+++" file-header lines
 * are recognized as headers while `inHeader` is true: `inHeader` turns false on
 * the first "@@" hunk header, or immediately after a "+++" line following a
 * "---" line (`seenOldHeader`) so flat single-edit diffs with no "@@" still
 * classify subsequent "---"/"+++" content lines as deletions/additions. A
 * "diff --git" or "index " line re-arms header detection for the next file.
 * Mirrors appwire-client/typescript/editDiff.ts's `classifyDiffLineKinds` so
 * DiffBlock stays self-contained while matching diffStats and mobile StepEvidence.
 */
function parseUnifiedDiff(unified: string): DiffLine[] {
  if (unified === "") return [];

  // Strip a trailing \r from every line before anything else looks at it -
  // a CRLF-diff's raw lines otherwise carry it into rendered content, AND
  // (more subtly) into the bare "---"/"+++" exact-match check just below,
  // which would silently fail on "---\r" and misclassify a CRLF file's own
  // header pair as a deletion/addition instead.
  const rawLines = unified.split("\n").map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
  // A single trailing newline is a formatting artifact, not a blank line
  // of real diff content.
  if (rawLines[rawLines.length - 1] === "") rawLines.pop();

  const lines: DiffLine[] = [];
  let inHeader = true;
  let seenOldHeader = false;

  for (const raw of rawLines) {
    if (raw.startsWith("diff --git") || raw.startsWith("index ")) {
      lines.push({ kind: "header", marker: "", content: raw });
      inHeader = true;
      seenOldHeader = false;
    } else if (raw.startsWith("@@")) {
      lines.push({ kind: "header", marker: "", content: raw });
      inHeader = false;
      seenOldHeader = false;
    } else if (inHeader && (raw.startsWith("--- ") || raw === "---")) {
      lines.push({ kind: "header", marker: "", content: raw });
      seenOldHeader = true;
    } else if (inHeader && (raw.startsWith("+++ ") || raw === "+++")) {
      lines.push({ kind: "header", marker: "", content: raw });
      if (seenOldHeader) {
        inHeader = false;
        seenOldHeader = false;
      }
    } else if (raw === NO_NEWLINE_MARKER) {
      lines.push({ kind: "meta", marker: "", content: raw });
    } else if (raw.startsWith("+")) {
      lines.push({ kind: "add", marker: "+", content: raw.slice(1) });
    } else if (raw.startsWith("-")) {
      lines.push({ kind: "del", marker: "-", content: raw.slice(1) });
    } else {
      // A context line's marker is a single leading space per the unified
      // diff format; a genuinely blank line has no marker to strip.
      const content = raw.startsWith(" ") ? raw.slice(1) : raw;
      lines.push({ kind: "context", marker: " ", content });
    }
  }

  return lines;
}

/**
 * Renders unified-diff text with per-line domain notation: additions and
 * deletions use dedicated quiet diff tints, while file/hunk headers remain
 * muted. The +/− marker is an independent meaning channel. No external
 * diff library - this only classifies already-diffed text; it doesn't compute
 * a diff of its own.
 */
export function DiffBlock({ unified }: DiffBlockProps) {
  const lines = useMemo(() => parseUnifiedDiff(unified), [unified]);

  return (
    <div className={CLASS.root}>
      <CodeBlockHeader action={<CopyButton text={unified} label="Copy diff" />} />
      {lines.map((line, i) => (
        // A diff line's position is intrinsically sequential (line 5 of a
        // unified diff IS line 5) and content can legitimately repeat
        // (e.g. two blank context lines) - i is both stable and collision-
        // free here, unlike a content-based key would be.
        // biome-ignore lint/suspicious/noArrayIndexKey: positionally sequential, content can repeat, see above
        <div key={i} className={`${CLASS.line} ${KIND_CLASS[line.kind]}`}>
          <span className={CLASS.marker} aria-hidden="true">
            {line.marker}
          </span>
          <span className={CLASS.content}>{line.content}</span>
        </div>
      ))}
    </div>
  );
}
