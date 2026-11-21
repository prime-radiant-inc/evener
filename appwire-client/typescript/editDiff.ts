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

// Sorted repair aliases matching agent/internal/tool/repair/repair.go and
// agent/transcript_render.go so canonical-first precedence and alias-conflict
// rejection are identical across Go and TypeScript.
const EDIT_FIELD_ALIASES: Record<"old_string" | "new_string" | "replace_all", readonly string[]> = {
  old_string: ["oldText", "old_str", "old_text"],
  new_string: ["newText", "new_str", "new_text"],
  replace_all: ["replaceAll"],
};

const ROOT_EDIT_CONFLICT_KEYS: readonly string[] = [
  "old_string",
  ...EDIT_FIELD_ALIASES.old_string,
  "new_string",
  ...EDIT_FIELD_ALIASES.new_string,
  "replace_all",
  ...EDIT_FIELD_ALIASES.replace_all,
];

const NO_NEWLINE_MARKER = "\\ No newline at end of file";

type FieldLookup = { found: true; value: unknown } | { found: false; conflict: boolean };

function readEditField(obj: Record<string, unknown>, canonical: keyof typeof EDIT_FIELD_ALIASES): FieldLookup {
  if (Object.hasOwn(obj, canonical) && obj[canonical] !== undefined) {
    return { found: true, value: obj[canonical] };
  }
  let found = false;
  let value: unknown;
  for (const alias of EDIT_FIELD_ALIASES[canonical]) {
    if (!Object.hasOwn(obj, alias) || obj[alias] === undefined) continue;
    const candidate = obj[alias];
    if (found && !Object.is(value, candidate)) {
      return { found: false, conflict: true };
    }
    value = candidate;
    found = true;
  }
  return found ? { found: true, value } : { found: false, conflict: false };
}

function parseReplaceAll(raw: unknown): boolean | null {
  if (typeof raw === "boolean") return raw;
  if (typeof raw === "string") {
    const lower = raw.toLowerCase();
    if (lower === "true") return true;
    if (lower === "false") return false;
  }
  return null;
}

function decodeEditOperation(obj: Record<string, unknown>, requireBoth: boolean): EditOperation | null {
  const oldField = readEditField(obj, "old_string");
  if (!oldField.found && oldField.conflict) return null;
  const newField = readEditField(obj, "new_string");
  if (!newField.found && newField.conflict) return null;

  if (requireBoth) {
    if (!oldField.found || !newField.found) return null;
  } else if (!oldField.found && !newField.found) {
    return null;
  }

  let oldString = "";
  if (oldField.found) {
    if (typeof oldField.value !== "string") return null;
    oldString = oldField.value;
  }

  let newString = "";
  if (newField.found) {
    if (typeof newField.value !== "string") return null;
    newString = newField.value;
  }

  const replaceAllField = readEditField(obj, "replace_all");
  if (!replaceAllField.found && replaceAllField.conflict) return null;

  if (replaceAllField.found) {
    const replaceAll = parseReplaceAll(replaceAllField.value);
    if (replaceAll === null) return null;
    return { oldString, newString, replaceAll };
  }

  return { oldString, newString };
}

function unwrapEditItems(raw: unknown): unknown[] | null {
  let current = raw;
  while (typeof current === "string") {
    try {
      current = JSON.parse(current) as unknown;
    } catch {
      return null;
    }
  }
  if (Array.isArray(current)) return current;
  if (isPlainObject(current)) return [current];
  return null;
}

// Extracts edit operations from edit_file / memory_edit arguments while
// preserving original raw arguments in stored transcripts.
export function editOperationsOf(args: Record<string, unknown>): EditOperation[] | null {
  const hasEdits = Object.hasOwn(args, "edits") && args.edits !== undefined;
  if (!hasEdits) {
    const legacy = decodeEditOperation(args, false);
    return legacy ? [legacy] : null;
  }

  for (const key of ROOT_EDIT_CONFLICT_KEYS) {
    if (Object.hasOwn(args, key) && args[key] !== undefined) return null;
  }

  const items = unwrapEditItems(args.edits);
  if (items === null || items.length === 0) return null;

  const ops: EditOperation[] = [];
  for (const item of items) {
    if (!isPlainObject(item)) return null;
    const op = decodeEditOperation(item, true);
    if (op === null) return null;
    ops.push(op);
  }
  return ops;
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
