// Two shared tool-body shapes, reused across several per-tool descriptors
// (fsTools.tsx, shellTool.tsx) rather than duplicated per file. Ground truth
// (see helpers.ts's own header): a tool call's ItemModel carries output text,
// input args, error text, and optional direct producer state in item.raw.
// These shared bodies intentionally render item.output because read_file can
// return text or an image, grep/list_dir/glob return plain text, and shell's
// structured state is not a common body shape. ToolCallItem surfaces error
// text and failed-row treatment generically; a domain-specific body uses
// item.raw only when its producer state is stable and materially improves the
// display.
import { CodeBlock } from "../../../../widgets";
import { clip, tailFold, tailSlice } from "./helpers";

interface OutputBodyProps {
  item: { output?: string };
  live: boolean;
}

const HEAD_CLIP_MAX_CHARS = 8000;
const TAIL_MAX_CHARS = 8000;

// HeadClippedOutputBody mirrors renderer-tools.js's generic cheapToolBody/
// cheapToolBodyDelta/cheapToolBodyEnd shape (grep/ls/glob): a plain head
// clip at 8000 chars, no elision note either way - these tools' output is
// small and bounded in the overwhelming case, so head vs. tail doesn't
// matter the way it does for a long-running read/shell stream.
export function HeadClippedOutputBody({ item }: OutputBodyProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <CodeBlock text={clip(output, HEAD_CLIP_MAX_CHARS)} copyLabel="Copy output" />;
}

// TailFoldedOutputBody mirrors renderer-tools.js's read_file/shell-specific
// bodies: while live, shows the raw last-8000-char TAIL (so a human
// watching a long stream sees the most recent activity, not a frozen
// head); once settled, folds to the same tail budget WITH an honest
// elision line. The error text on a failed call is surfaced by ToolCallItem
// above the body (see this file's own header), so this body always renders
// item.output on the tail-fold path regardless of outcome.
export function TailFoldedOutputBody({ item, live }: OutputBodyProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const text = live ? tailSlice(output, TAIL_MAX_CHARS) : tailFold(output, TAIL_MAX_CHARS);
  return <CodeBlock text={text} copyLabel="Copy output" />;
}
