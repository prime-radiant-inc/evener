// Two shared tool-body shapes, reused across several per-tool descriptors
// (fsTools.tsx, shellTool.tsx) rather than duplicated per file. Ground truth
// (see helpers.ts's own header): a tool call's ItemModel carries only its
// output TEXT and input args, never a structured tool_state/error - both
// bodies below render straight off `item.output` with no error/success
// branching, since that signal isn't available at this layer.
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
// elision line. Legacy additionally head-clips on error instead of
// tail-folding on success - unreachable here since ItemModel drops the
// wire's error field entirely (see this file's own header and the task
// report's ground-truth section), so this always takes the tail-fold path.
export function TailFoldedOutputBody({ item, live }: OutputBodyProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const text = live ? tailSlice(output, TAIL_MAX_CHARS) : tailFold(output, TAIL_MAX_CHARS);
  return <CodeBlock text={text} />;
}
