import { useMemo } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./diffblock.module.css";

export interface DiffBlockProps {
  unified: string;
}

type DiffLineKind = "header" | "add" | "del" | "context";

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
};

const KIND_CLASS: Record<DiffLineKind, string> = {
  header: CLASS.header,
  add: CLASS.add,
  del: CLASS.del,
  context: CLASS.context,
};

/**
 * Classifies each line of unified-diff text. "---"/"+++" file-header lines
 * are only recognized as headers BEFORE the first "@@" hunk of a file
 * block (tracked via `inHeader`): after that, a content line that itself
 * starts with "---" or "+++" (a deleted/added line whose original text
 * happened to start that way) is unambiguously a deletion/addition, never
 * mistaken for a second header - a real ambiguity in the unified diff
 * format that a naive per-line prefix check gets wrong. A "diff --git"
 * line starts a new file block in a multi-file diff and re-arms header
 * detection for that file's own "---"/"+++" pair.
 */
function parseUnifiedDiff(unified: string): DiffLine[] {
  if (unified === "") return [];

  const rawLines = unified.split("\n");
  // A single trailing newline is a formatting artifact, not a blank line
  // of real diff content.
  if (rawLines[rawLines.length - 1] === "") rawLines.pop();

  const lines: DiffLine[] = [];
  let inHeader = true;

  for (const raw of rawLines) {
    if (raw.startsWith("diff --git") || raw.startsWith("index ")) {
      lines.push({ kind: "header", marker: "", content: raw });
      inHeader = true;
    } else if (raw.startsWith("@@")) {
      lines.push({ kind: "header", marker: "", content: raw });
      inHeader = false;
    } else if (inHeader && (raw.startsWith("--- ") || raw === "---" || raw.startsWith("+++ ") || raw === "+++")) {
      lines.push({ kind: "header", marker: "", content: raw });
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
 * Renders unified-diff text with per-line tone: additions alive-tinted,
 * deletions danger-tinted, file/hunk headers muted. No external diff
 * library - this only classifies already-diffed text; it doesn't compute
 * a diff of its own.
 */
export function DiffBlock({ unified }: DiffBlockProps) {
  const lines = useMemo(() => parseUnifiedDiff(unified), [unified]);

  return (
    <div className={CLASS.root}>
      {lines.map((line, i) => (
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
