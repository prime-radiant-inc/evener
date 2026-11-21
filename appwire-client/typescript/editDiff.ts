// Diff text and argument normalization for the file-editing tools, shared by
// the web and the phone. edit_file's diff is synthesized from its edit
// operations: a flat diff for a single edit, or @@ edit N @@ hunks for a
// batch. apply_patch carries its own patch text. write_file has no prior
// content anywhere on the wire, so it has no diff.

import { isPlainObject } from "./plainObject";

export interface EditOperation {
  readonly oldString: string;
  readonly newString: string;
  readonly replaceAll?: boolean;
}

export type DiffLineKind = "header" | "add" | "del" | "context" | "meta";

const NO_NEWLINE_MARKER = "\\ No newline at end of file";

// Extracts edit operations from canonical edits[] or historical top-level
// old_string/new_string edit_file / memory_edit arguments.
export function editOperationsOf(args: Record<string, unknown>): EditOperation[] | null {
  if ("edits" in args) {
    if (!Array.isArray(args.edits) || args.edits.length === 0) return null;
    const ops: EditOperation[] = [];
    for (const item of args.edits) {
      if (!isPlainObject(item)) return null;
      if (typeof item.old_string !== "string" || typeof item.new_string !== "string") return null;
      if (item.replace_all !== undefined && typeof item.replace_all !== "boolean") return null;
      ops.push(
        item.replace_all !== undefined
          ? { oldString: item.old_string, newString: item.new_string, replaceAll: item.replace_all }
          : { oldString: item.old_string, newString: item.new_string },
      );
    }
    return ops;
  }
  const old = typeof args.old_string === "string" ? args.old_string : undefined;
  const next = typeof args.new_string === "string" ? args.new_string : undefined;
  if (old === undefined && next === undefined) return null;
  return [
    {
      oldString: old ?? "",
      newString: next ?? "",
      ...(typeof args.replace_all === "boolean" ? { replaceAll: args.replace_all } : {}),
    },
  ];
}

// Single diff-synthesis implementation for one or more edits to a single file.
// For a single edit, output is identical to editDiffText (no @@ header).
// For multiple edits, each edit is separated by an "@@ edit N @@" header.
export function editBatchDiffText(path: string, edits: readonly EditOperation[]): string {
  if (edits.length === 0) return "";
  const lines = [`--- ${path}`, `+++ ${path}`];
  const multi = edits.length > 1;
  for (const [index, edit] of edits.entries()) {
    if (multi) lines.push(`@@ edit ${index + 1} @@`);
    for (const line of edit.oldString.split("\n")) lines.push(`-${line}`);
    for (const line of edit.newString.split("\n")) lines.push(`+${line}`);
  }
  return lines.join("\n");
}

// editDiffText is a flat synthesized diff (no real @@ hunk range) - every
// old_string line prefixed "-", every new_string line prefixed "+", framed by
// "---"/"+++" file-name headers.
export function editDiffText(path: string, oldString: string, newString: string): string {
  return editBatchDiffText(path, [{ oldString, newString }]);
}

// Classifies each line of unified-diff text using DiffBlock's exact line kinds
// while ending header mode immediately after the leading --- / +++ pair on
// flat single-edit diffs with no @@.
export function classifyDiffLineKinds(unified: string): DiffLineKind[] {
  if (unified === "") return [];

  const rawLines = unified.split("\n").map((line) => (line.endsWith("\r") ? line.slice(0, -1) : line));
  if (rawLines[rawLines.length - 1] === "") rawLines.pop();

  const kinds: DiffLineKind[] = [];
  let inHeader = true;
  let sawMinusHeader = false;

  for (const raw of rawLines) {
    if (raw.startsWith("diff --git") || raw.startsWith("index ")) {
      kinds.push("header");
      inHeader = true;
      sawMinusHeader = false;
    } else if (raw.startsWith("@@")) {
      kinds.push("header");
      inHeader = false;
      sawMinusHeader = false;
    } else if (inHeader && (raw.startsWith("--- ") || raw === "---")) {
      kinds.push("header");
      sawMinusHeader = true;
    } else if (inHeader && (raw.startsWith("+++ ") || raw === "+++")) {
      kinds.push("header");
      if (sawMinusHeader) {
        inHeader = false;
        sawMinusHeader = false;
      }
    } else if (raw === NO_NEWLINE_MARKER) {
      kinds.push("meta");
    } else if (raw.startsWith("+")) {
      kinds.push("add");
    } else if (raw.startsWith("-")) {
      kinds.push("del");
    } else {
      kinds.push("context");
    }
  }

  return kinds;
}

// diffStats counts add/del lines using the shared header-aware classifier.
export function diffStats(text: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const kind of classifyDiffLineKinds(text)) {
    if (kind === "add") added += 1;
    else if (kind === "del") removed += 1;
  }
  return { added, removed };
}
