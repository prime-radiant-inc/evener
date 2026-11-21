import { classifyDiffLineKinds, type DiffLineKind } from "@evener/appwire-client";
import { useMemo } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./diffblock.module.css";

export interface DiffBlockProps {
  unified: string;
}

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

/**
 * Parses unified-diff text into DiffLine records using @evener/appwire-client's
 * shared classifyDiffLineKinds classifier so DiffBlock agrees 100% with
 * diffStats and mobile StepEvidence.
 */
function parseUnifiedDiff(unified: string): DiffLine[] {
  if (unified === "") return [];

  // Strip a trailing \r from every line so CRLF diffs do not carry \r into
  // rendered content.
  const rawLines = unified.split("\n").map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
  // A single trailing newline is a formatting artifact, not a blank line
  // of real diff content.
  if (rawLines[rawLines.length - 1] === "") rawLines.pop();

  const kinds = classifyDiffLineKinds(unified);
  return rawLines.map((raw, i) => {
    const kind = kinds[i] ?? "context";
    switch (kind) {
      case "header":
      case "meta":
        return { kind, marker: "", content: raw };
      case "add":
        return { kind, marker: "+", content: raw.slice(1) };
      case "del":
        return { kind, marker: "-", content: raw.slice(1) };
      default: {
        // A context line's marker is a single leading space per the unified
        // diff format; a genuinely blank line has no marker to strip.
        const content = raw.startsWith(" ") ? raw.slice(1) : raw;
        return { kind, marker: " ", content };
      }
    }
  });
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
