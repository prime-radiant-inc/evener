import { editDiffText } from "./editDiff";
import type { ItemModel } from "./model";
import {
  BINARY_PAYLOAD_HEADER,
  diffResultText,
  outputCount,
  quotedSearchPattern,
  readLineRange,
  type StepWords,
  summaryOf,
  withDetail,
} from "./stepWords";
import { parseArgs, str } from "./toolCallText";

/** The parts of a memory step its words read. */
export type MemoryStep = Pick<ItemModel, "argumentsJSON" | "output">;

// "session" stays so transcripts from earlier builds keep their scope label.
const MEMORY_SCOPES = new Set(["personal", "project", "session"]);

function scopeOf(args: Record<string, unknown>): string | undefined {
  const scope = str(args, "scope");
  return scope && MEMORY_SCOPES.has(scope) ? scope : undefined;
}

function filePathOf(args: Record<string, unknown>): string | undefined {
  return str(args, "file_path") || str(args, "path") || undefined;
}

// Memory paths are not workspace paths: carry their scope when known, and
// retain the bare path for historical calls whose arguments lack a scope.
export function memoryFileTarget(args: Record<string, unknown>): string | undefined {
  const path = filePathOf(args);
  if (!path) return undefined;
  const scope = scopeOf(args);
  return scope ? `${scope}/${path}` : path;
}

function memorySearchLocation(args: Record<string, unknown>): string | undefined {
  const scope = scopeOf(args);
  const path = str(args, "path");
  const subpath = path && path !== "." ? path : undefined;
  if (scope && subpath) return `${scope}/${subpath}`;
  return scope ?? subpath;
}

function memoryArgs(step: Pick<MemoryStep, "argumentsJSON">): Record<string, unknown> {
  return parseArgs(step.argumentsJSON);
}

export function memoryWriteWords(step: MemoryStep): StepWords {
  const target = memoryFileTarget(memoryArgs(step));
  return target ? { verb: "Wrote memory", target } : { verb: "Wrote memory" };
}

export function memoryEditWords(step: MemoryStep): StepWords {
  const args = memoryArgs(step);
  const target = memoryFileTarget(args);
  if (!target) return { verb: "Edited memory" };
  const oldString = str(args, "old_string");
  const newString = str(args, "new_string");
  // Neither side present is the malformed shape the clients' bodies fall
  // back on; there is no diff to describe, so no detail rides the summary.
  if (oldString === undefined && newString === undefined) return { verb: "Edited memory", target };
  const path = filePathOf(args) ?? "";
  const diff = editDiffText(path, oldString ?? "", newString ?? "");
  return { verb: "Edited memory", target, detail: diffResultText(diff) };
}

export function memoryReadWords(step: MemoryStep): StepWords {
  const args = memoryArgs(step);
  const target = memoryFileTarget(args);
  if (!target) return { verb: "Read memory" };
  // A binary read's output is only the "[image: ...]" / "[document: ...]"
  // header - one metadata line, not page content - so the summary omits the
  // range exactly as readFileWords does for the same bytes.
  if (BINARY_PAYLOAD_HEADER.test(step.output ?? "")) return { verb: "Read memory", target };
  return withDetail({ verb: "Read memory", target }, readLineRange(args, step.output ?? ""));
}

export function memorySearchWords(step: MemoryStep): StepWords {
  const args = memoryArgs(step);
  const pattern = str(args, "pattern");
  if (!pattern) return { verb: "Searched memory" };
  const location = memorySearchLocation(args);
  return withDetail(
    {
      verb: "Searched memory for",
      target: quotedSearchPattern(pattern),
      ...(location ? { after: `in ${location}` } : {}),
    },
    outputCount(step.output, "hits"),
  );
}

export function memoryDeleteWords(step: MemoryStep): StepWords {
  const target = memoryFileTarget(memoryArgs(step));
  return target ? { verb: "Removed memory", target } : { verb: "Removed memory" };
}

/** Whether a memory tool call changes a page, not only reads one: the write,
 * edit and delete operations. Run-line summaries read this, so a memory
 * mutator added here is classified where its words are. */
export function memoryMutation(toolName: string): boolean {
  return toolName === "memory_write" || toolName === "memory_edit" || toolName === "memory_delete";
}

/** What a running memory tool call is doing in the tray. */
export function memoryProgress(toolName: string, step: Pick<MemoryStep, "argumentsJSON">): string | undefined {
  const args = memoryArgs(step);
  const target = memoryFileTarget(args);
  switch (toolName) {
    case "memory_write":
      return target ? `Writing memory ${target}` : "Writing memory";
    case "memory_edit":
      return target ? `Editing memory ${target}` : "Editing memory";
    case "memory_read":
      return target ? `Reading memory ${target}` : "Reading memory";
    case "memory_search": {
      const pattern = str(args, "pattern");
      if (!pattern) return "Searching memory";
      const location = memorySearchLocation(args);
      return `Searching memory for ${quotedSearchPattern(pattern)}${location ? ` in ${location}` : ""}`;
    }
    case "memory_delete":
      return target ? `Removing memory ${target}` : "Removing memory";
    default:
      return undefined;
  }
}

export const memoryWriteSummary = summaryOf(memoryWriteWords);
export const memoryEditSummary = summaryOf(memoryEditWords);
export const memoryReadSummary = summaryOf(memoryReadWords);
export const memorySearchSummary = summaryOf(memorySearchWords);
export const memoryDeleteSummary = summaryOf(memoryDeleteWords);
