// The tool-renderer registry: one descriptor per tool name (or a job_*-style
// predicate family), keyed off ItemModel.toolName for "commandExecution"
// items. Wave 4 T1 ships the registry + the raw-output fallback; T3
// registers the real per-tool descriptors (read/grep/ls/glob/shell/diff/
// patch/web fetch+search/delegate/job_*/ask_user/sandbox escalation).
import type { ComponentType } from "react";
import type { ItemModel } from "../../../protocol/model";
import { RawToolOutput } from "./RawToolOutput";

export interface ToolRenderProps {
  item: ItemModel;
  live: boolean;
}

export interface ToolRendererDescriptor {
  match: string | ((toolName: string) => boolean); // exact name or predicate (job_* family)
  summary(item: ItemModel): string; // one-line purpose-first summary
  body?: ComponentType<ToolRenderProps>; // expanded content; default raw output
  autoExpand?(item: ItemModel): boolean; // e.g. shell on nonzero exit
  // failed is a TOOL-SPECIFIC failure signal, OR'd with the generic one
  // ToolCallItem derives from ItemModel.error/status. It exists because a
  // clean tool RESULT can still report a failed action: a shell command that
  // ran and exited nonzero carries no ItemModel.error at all (the wire stamps
  // status "completed"), yet it is exactly what the reader needs the failure
  // glyph for. Mirrors the legacy renderer's own toolLooksGood, which likewise
  // treated a nonzero exit_code as not-good (renderer-format.js:593).
  failed?(item: ItemModel): boolean;
  // detail is secondary fact the row must keep REACHABLE without making it the
  // headline - rendered as the row's hover title. The shell exit code is the
  // motivating case (A2: "exit 1" stops being the failure signal, the glyph is).
  detail?(item: ItemModel): string | undefined;
  // suppress removes the whole tool-call row from the transcript when true -
  // no summary, no body, nothing (ToolCallItem renders null). Used for a
  // task_list `action:"view"` (a read that legacy renders nothing for) and a
  // malformed non-mutation call: the same "fully suppressed - no card, no
  // divider, no tool-call row" the legacy renderer applied. An errored call is
  // never suppressed here, so its error still surfaces via the generic
  // failed-row treatment in ToolCallItem.
  suppress?(item: ItemModel): boolean;
  // openBesidePath returns the single file path this tool card references (its
  // file arg), or undefined when it references none - the ONLY tools that opt in
  // are the single-file ones (read_file/edit_file/write_file, floor §3.7;
  // multi-target apply_patch and directory/pattern grep/ls/glob are excluded).
  // ToolCallItem turns a non-undefined path into an "open beside" affordance
  // (relativized against the session cwd, out-of-cwd gated - fileOpenBeside.tsx).
  openBesidePath?(item: ItemModel): string | undefined;
}

const registry: ToolRendererDescriptor[] = [];

export function registerToolRenderer(d: ToolRendererDescriptor): void {
  registry.push(d);
}

// DEFAULT_DESCRIPTOR mirrors legacy renderer-tools.js's own
// toolRenderers.__default__ / defaultRenderer (renderer-tools.js:773-778):
// used for any tool name with no registered descriptor.
const DEFAULT_DESCRIPTOR: ToolRendererDescriptor = {
  match: () => true,
  summary: (item) => item.toolName ?? "tool",
  body: RawToolOutput,
};

// toolRendererFor mirrors legacy renderer-tools.js's own toolRendererFor
// name (renderer-tools.js:17-19). Exact-string matches are checked across
// the WHOLE registry before any predicate, so a specific descriptor
// (e.g. "job_stop") always wins over a broader family predicate
// (e.g. name.startsWith("job_")) regardless of registration order; among
// same-kind matches (two predicates, or two identical exact strings), the
// first-registered one wins. Falls back to DEFAULT_DESCRIPTOR, never throws
// - an unregistered tool name is an everyday case (most tools have no
// dedicated descriptor until T3 lands), not a bug.
export function toolRendererFor(toolName: string): ToolRendererDescriptor {
  const exact = registry.find((d) => d.match === toolName);
  if (exact) return exact;
  const predicate = registry.find((d) => typeof d.match === "function" && d.match(toolName));
  return predicate ?? DEFAULT_DESCRIPTOR;
}
