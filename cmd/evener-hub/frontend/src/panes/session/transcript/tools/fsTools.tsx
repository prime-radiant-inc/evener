// Descriptors for the four read-only filesystem tools (parity checklist §2):
// read_file, grep (+ its grep_files/grep_search aliases), list_dir (+
// list_directory), glob. Each is "cheap" mode in the legacy sense (a short,
// bounded row) - target/result folded into one intent-first summary string
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

// read_file's output for an image/PDF read is a "[image: FORMAT, N bytes,
// base64 data follows]" (or "[document: ...]") header: registry.go's
// ParseImageResult cuts the string at its first "\n" and routes the bytes
// elsewhere, so nothing ever "follows" here (kata 1nr4). An image read's
// body renders nothing - the image displays at this descriptor's
// outputImageSize ("large"), and the header is noise next to the picture.
// A PDF has no such preview, so its body keeps the header minus the stale
// phrase (and minus any payload an older daemon left in output).
const BINARY_PAYLOAD_HEADER = /^\[(image|document): [^\]]+, base64 data follows\]/;

// isImageRead is the single source of truth for "this read_file call's output
// is a picture, not text": the body renders nothing for it (the ImageGallery
// carries the picture below), and autoExpand opens the row by default so the
// picture is visible without a click. Sharing one predicate keeps the two
// decisions from ever disagreeing - the body that renders nothing and the
// auto-open that shows it both answer the same question.
function isImageRead(item: ItemModel): boolean {
  const match = BINARY_PAYLOAD_HEADER.exec(item.output ?? "");
  return match !== null && match[1] === "image";
}

function ReadFileOutputBody({ item, live }: ToolRenderProps) {
  const output = item.output ?? "";
  const match = BINARY_PAYLOAD_HEADER.exec(output);
  if (match === null) return <TailFoldedOutputBody item={item} live={live} />;
  if (isImageRead(item)) return null;
  return <CodeBlock text={match[0].replace(", base64 data follows]", "]")} copyLabel="Copy output" />;
}

// The bare file-path text, shared between summary() and openBesideInline()
// so the two stay byte-for-byte consistent: openBesideInline hands ToolRow
// the exact "Read <target>" prefix summary() itself emits, not a fragment
// ToolRow would have to go search for. A bare substring search is ambiguous
// whenever the target text recurs elsewhere in the summary, e.g. a file
// literally named "lines" colliding with readLineRange's own "lines N-M"
// meta text below (kata ledger #97).
function readFileTarget(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  return str(args, "file_path") ?? str(args, "path") ?? "";
}

registerToolRenderer({
  match: "read_file",
  icon: "file",
  fold: "quiet",
  // An image read displays the picture itself at up to 600px square, not a
  // 96px thumbnail - the picture IS this call's output.
  outputImageSize: "large",
  // An image read auto-expands: the picture is the call's whole output, so it
  // should be visible without a click. A text or PDF read keeps the usual
  // collapsed default - isImageRead shares the body's own detection so the
  // auto-open and the empty body can never disagree (the one can't open while
  // the other still renders text for the same call).
  autoExpand: isImageRead,
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    return `Read ${readFileTarget(item)} · ${readLineRange(args, item.output ?? "")}`;
  },
  body: ReadFileOutputBody,
  // read_file references a single file (floor §3.7): expose it for the "open
  // beside" affordance. grep/list_dir/glob below reference a directory or
  // pattern, not a single file, so they opt OUT (no openBesidePath).
  openBesidePath: (item) => {
    const args = parseArgs(item.argumentsJSON);
    return str(args, "file_path") ?? str(args, "path");
  },
  // The summary quotes the path verbatim between the verb and the line range
  // ("Read <path> · lines N-M"), so the "open beside" control rides INLINE
  // between the file name and the range it opens (toolRenderers.ts's
  // openBesideInline contract) - the complete "Read <path>" prefix, matching
  // summary()'s own text exactly, so ToolRow can verify it with startsWith
  // rather than search for it.
  openBesideInline: (item) => `Read ${readFileTarget(item)}`,
});

function grepTarget(args: Record<string, unknown>): string {
  const pattern = clip(str(args, "pattern") ?? "", GREP_PATTERN_CLIP);
  const path = str(args, "path") ?? ".";
  const globFilter = str(args, "glob_filter");
  return `"${pattern}" in ${path}${globFilter ? ` (${globFilter})` : ""}`;
}

const grepDescriptor: ToolRendererDescriptor = {
  match: (name: string) => name === "grep" || name === "grep_files" || name === "grep_search",
  fold: "quiet",
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
  fold: "quiet",
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
  fold: "quiet",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const pattern = str(args, "pattern") ?? str(args, "glob") ?? "";
    return `Matched ${pattern} · ${lineCount(item.output ?? "")} matches`;
  },
  body: HeadClippedOutputBody,
});
