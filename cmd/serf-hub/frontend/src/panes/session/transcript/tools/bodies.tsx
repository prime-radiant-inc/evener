// Two shared tool-body shapes, reused across several per-tool descriptors
// (fsTools.tsx, shellTool.tsx) rather than duplicated per file. Ground truth
// (see helpers.ts's own header): a tool call's ItemModel carries its output
// TEXT (item.output), input args, and — on a failed/denied call — its error
// text (item.error). These bodies render straight off `item.output`; the
// error text and the failed-row treatment (force-open, failure marker) are
// surfaced GENERICALLY once by ToolCallItem for every descriptor, so a body
// never has to branch on error/success itself. The tool_state snapshot the
// legacy renderer-tools.js relied on is still dropped by the reducer.
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
  return <CodeBlock text={clip(output, HEAD_CLIP_MAX_CHARS)} />;
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
  return <CodeBlock text={text} />;
}
