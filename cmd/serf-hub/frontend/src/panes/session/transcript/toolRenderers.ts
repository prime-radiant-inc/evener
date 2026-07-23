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
  // suppress removes the whole tool-call row from the transcript when true -
  // no summary, no body, nothing (ToolCallItem renders null). Used for a
  // task_list `action:"view"` (a read that legacy renders nothing for) and a
  // malformed non-mutation call: the same "fully suppressed - no card, no
  // divider, no tool-call row" the legacy renderer applied. An errored call is
  // never suppressed here, so its error still surfaces via the generic
  // failed-row treatment in ToolCallItem.
  suppress?(item: ItemModel): boolean;
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
