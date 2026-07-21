// Descriptors for the four read-only filesystem tools (parity checklist §2):
// read_file, grep (+ its grep_files/grep_search aliases), list_dir (+
// list_directory), glob. Each is "cheap" mode in the legacy sense (a short,
// bounded row) - target/result folded into one purpose-first summary string
// per this file's own ToolRendererDescriptor contract (there is no separate
// target/result slot on the wire like the legacy DOM had).

import type { ItemModel } from "../../../../protocol/model";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";
import { clip, lineCount, parseArgs, str } from "./helpers";

const GREP_PATTERN_CLIP = 50;

// readLineRange mirrors renderer-tools.js's own readLineRange: offset
// defaults to 1 when absent/non-positive; the line count defaults to the
// number of "\n" characters in the output (NOT lineCount()'s "drop one
// trailing blank" rule - this counts raw newlines, matching the legacy
// helper's documented behavior) when no explicit `limit` arg is given.
function readLineRange(args: Record<string, unknown>, output: string): string {
  const offsetArg = args.offset;
  const offset = typeof offsetArg === "number" && offsetArg > 0 ? offsetArg : 1;
  const limitArg = args.limit;
  const count = typeof limitArg === "number" && limitArg > 0 ? limitArg : (output.match(/\n/g) ?? []).length;
  return count > 0 ? `lines ${offset}-${offset + count - 1}` : `lines ${offset}`;
}

registerToolRenderer({
  match: "read_file",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const target = str(args, "file_path") ?? str(args, "path") ?? "";
    return `Read ${target} · ${readLineRange(args, item.output ?? "")}`;
  },
  body: TailFoldedOutputBody,
});

function grepTarget(args: Record<string, unknown>): string {
  const pattern = clip(str(args, "pattern") ?? "", GREP_PATTERN_CLIP);
  const path = str(args, "path") ?? ".";
  const globFilter = str(args, "glob_filter");
  return `"${pattern}" in ${path}${globFilter ? ` (${globFilter})` : ""}`;
}

const grepDescriptor = {
  match: (name: string) => name === "grep" || name === "grep_files" || name === "grep_search",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    return `Searched ${grepTarget(args)} · ${lineCount(item.output ?? "")} hits`;
  },
  body: HeadClippedOutputBody,
};
registerToolRenderer(grepDescriptor);

const lsDescriptor = {
  match: (name: string) => name === "list_dir" || name === "list_directory",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const path = str(args, "path") ?? ".";
    const pattern = str(args, "pattern");
    return `Listed ${path}${pattern ? ` (${pattern})` : ""} · ${lineCount(item.output ?? "")} entries`;
  },
  body: HeadClippedOutputBody,
};
registerToolRenderer(lsDescriptor);

registerToolRenderer({
  match: "glob",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const pattern = str(args, "pattern") ?? str(args, "glob") ?? "";
    return `Matched ${pattern} · ${lineCount(item.output ?? "")} matches`;
  },
  body: HeadClippedOutputBody,
});
