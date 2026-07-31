// Descriptors for the four read-only filesystem tools (parity checklist §2):
// read_file, grep (+ its grep_files/grep_search aliases), list_dir (+
// list_directory), glob. Each is "cheap" mode in the legacy sense (a short,
// bounded row) - target/result folded into one purpose-first summary string
// per this file's own ToolRendererDescriptor contract (there is no separate
// target/result slot on the wire like the legacy DOM had).

import type { ItemModel } from "../../../../protocol/model";
import { CodeBlock } from "../../../../widgets";
import { registerToolRenderer, type ToolRendererDescriptor, type ToolRenderProps } from "../toolRenderers";
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

// read_file's own output for an image/PDF read mirrors agent/execenv/
// local.go's ReadFile: a "[image: FORMAT, N bytes, base64 data follows]" (or
// "[document: ...]") header. The base64 payload never actually reaches this
// text - agent/internal/tool/registry.go's ParseImageResult cuts the
// ReadFile string at its first "\n" and keeps only this header as the tool
// result's own Output, routing the decoded bytes through a separate field -
// so "base64 data follows" is stale/misleading by the time a reader sees it
// (kata 1nr4): nothing ever follows it here. BINARY_PAYLOAD_HEADER detects
// that shape so ReadFileOutputBody, below, can show just the honest header
// (dropping that phrase, and dropping any payload that DID make it into
// output - an older daemon, say - rather than ever rendering it as text).
// The real image renders as a thumbnail alongside, via ToolCallItem's
// <ImageGallery images={item.outputImages} />, once the file resolves as a
// supported image (output_images.go's read_file case).
const BINARY_PAYLOAD_HEADER = /^\[(?:image|document): [^\]]+, base64 data follows\]/;

function ReadFileOutputBody({ item, live }: ToolRenderProps) {
  const output = item.output ?? "";
  const match = BINARY_PAYLOAD_HEADER.exec(output);
  if (match === null) return <TailFoldedOutputBody item={item} live={live} />;
  return <CodeBlock text={match[0].replace(", base64 data follows]", "]")} copyLabel="Copy output" />;
}

registerToolRenderer({
  match: "read_file",
  icon: "file",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const target = str(args, "file_path") ?? str(args, "path") ?? "";
    return `Read ${target} · ${readLineRange(args, item.output ?? "")}`;
  },
  body: ReadFileOutputBody,
  // read_file references a single file (floor §3.7): expose it for the "open
  // beside" affordance. grep/list_dir/glob below reference a directory or
  // pattern, not a single file, so they opt OUT (no openBesidePath).
  openBesidePath: (item) => {
    const args = parseArgs(item.argumentsJSON);
    return str(args, "file_path") ?? str(args, "path");
  },
});

function grepTarget(args: Record<string, unknown>): string {
  const pattern = clip(str(args, "pattern") ?? "", GREP_PATTERN_CLIP);
  const path = str(args, "path") ?? ".";
  const globFilter = str(args, "glob_filter");
  return `"${pattern}" in ${path}${globFilter ? ` (${globFilter})` : ""}`;
}

const grepDescriptor: ToolRendererDescriptor = {
  match: (name: string) => name === "grep" || name === "grep_files" || name === "grep_search",
  icon: "search",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    return `Searched ${grepTarget(args)} · ${lineCount(item.output ?? "")} hits`;
  },
  body: HeadClippedOutputBody,
};
registerToolRenderer(grepDescriptor);

const lsDescriptor: ToolRendererDescriptor = {
  match: (name: string) => name === "list_dir" || name === "list_directory",
  icon: "folder",
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
  icon: "search",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const pattern = str(args, "pattern") ?? str(args, "glob") ?? "";
    return `Matched ${pattern} · ${lineCount(item.output ?? "")} matches`;
  },
  body: HeadClippedOutputBody,
});
