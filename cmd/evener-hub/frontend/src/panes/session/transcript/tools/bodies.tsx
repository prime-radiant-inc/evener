// Shared tool-body shapes, reused across several per-tool descriptors
// (fsTools.tsx, shellTool.tsx, editTools.tsx, memoryTools.tsx) rather than
// duplicated per file. Ground truth
// (see protocol/toolCallText.ts's own header): a tool call's ItemModel carries
// output text, input args, error text, and optional direct producer state in
// item.raw.
// These shared bodies intentionally render item.output because read_file can
// return text or an image, grep/list_dir/glob return plain text, and shell's
// structured state is not a common body shape. ToolCallItem surfaces error
// text and failed-row treatment generically; a domain-specific body uses
// item.raw only when its producer state is stable and materially improves the
// display.

import { clip, editDiffText, parseArgs, str, tailFold, tailSlice } from "@evener/appwire-client";
import { CodeBlock, DiffBlock } from "../../../../widgets";
import { excerptAround, holdsNeedle } from "../../find/findExcerpt";
import { useFindReveal } from "../../find/findReveal";
import type { ToolRenderProps } from "../toolRenderers";

interface OutputBodyProps {
  // The id is how Find names the call it is landed on; a caller rendering
  // loose output (no item) has none and is never a landing.
  item: { id?: string; output?: string };
  live: boolean;
}

// The one budget for a tool call's expanded body text (issue #4015). The
// shared bodies here and the descriptors that clip their own body text all
// read it, so a retune moves the head clip, the tail fold and the shell tail
// together.
export const TOOL_BODY_MAX_CHARS = 8000;

// HeadClippedOutputBody mirrors renderer-tools.js's generic cheapToolBody/
// cheapToolBodyDelta/cheapToolBodyEnd shape (grep/ls/glob): a plain head clip
// at TOOL_BODY_MAX_CHARS, no elision note either way - these tools' output is
// small and bounded in the overwhelming case, so head vs. tail doesn't matter
// the way it does for a long-running read/shell stream.
export function HeadClippedOutputBody({ item }: OutputBodyProps) {
  const output = item.output ?? "";
  const needle = useFindReveal(item.id ?? "");
  if (output === "") return null;
  return <FoldedOutput output={output} shown={clip(output, TOOL_BODY_MAX_CHARS)} needle={needle} />;
}

// TailFoldedOutputBody mirrors renderer-tools.js's read_file/shell-specific
// bodies: while live, shows the raw last TOOL_BODY_MAX_CHARS chars (so a
// human watching a long stream sees the most recent activity, not a frozen
// head); once settled, folds to the same budget WITH an honest
// elision line. The error text on a failed call is surfaced by ToolCallItem
// above the body (see this file's own header), so this body always renders
// item.output on the tail-fold path regardless of outcome.
export function TailFoldedOutputBody({ item, live }: OutputBodyProps) {
  const output = item.output ?? "";
  const needle = useFindReveal(item.id ?? "");
  if (output === "") return null;
  const text = live ? tailSlice(output, TOOL_BODY_MAX_CHARS) : tailFold(output, TOOL_BODY_MAX_CHARS);
  return <FoldedOutput output={output} shown={text} needle={needle} />;
}

// A folded body normally shows `shown`, line-folded by the block. While Find
// is landed on words in this output it shows them: the block's line fold
// comes off, and where `shown` dropped the words it gives way to the same
// budget of text around them, under a line saying so.
function FoldedOutput({ output, shown, needle }: { output: string; shown: string; needle: string }) {
  const landing = needle === "" ? null : landingIn(output, shown, needle);
  if (landing === null) return <CodeBlock text={shown} copyLabel="Copy output" />;
  if (landing.excerpt === null) return <CodeBlock text={shown} copyLabel="Copy output" fold={false} />;
  return <Excerpt text={landing.excerpt} />;
}

/** For a body that keeps its own rendering of the tail: whether Find is
 * landed on words in this call's output (so its block must not line-fold),
 * and the stretch holding them when what is shown does not. The two texts are
 * asked for only while Find is landed on this call, so a body that has to
 * decode its output to answer does not pay for that on every render. */
export function useFindLanding(
  itemId: string,
  output: () => string,
  shown: () => string,
): { excerpt: string | null } | null {
  const needle = useFindReveal(itemId);
  return needle === "" ? null : landingIn(output(), shown(), needle);
}

// The excerpt is already bounded, so the block's own line fold stays off:
// folding it to its last lines could hide the match again.
export function Excerpt({ text, ansi }: { text: string; ansi?: boolean }) {
  return (
    <div data-find-excerpt="">
      <CodeBlock text={text} copyLabel="Copy output" fold={false} ansi={ansi} />
    </div>
  );
}

// Null when the output does not hold the words at all (the match is
// elsewhere in the call); a null excerpt when `shown` already holds them.
function landingIn(output: string, shown: string, needle: string): { excerpt: string | null } | null {
  if (!holdsNeedle(output, needle)) return null;
  if (holdsNeedle(shown, needle)) return { excerpt: null };
  return { excerpt: excerptAround(output, needle, TOOL_BODY_MAX_CHARS) };
}

// EditFileBody synthesizes an edit's diff from its old_string/new_string
// input args: the wire carries no diff of its own for edit_file or
// memory_edit, both of which share this shape.
export function EditFileBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const path = str(args, "file_path") ?? str(args, "path") ?? "";
  const oldString = str(args, "old_string") ?? "";
  const newString = str(args, "new_string") ?? "";
  return <DiffBlock unified={editDiffText(path, oldString, newString)} />;
}

// WriteFileBody renders a plain confirmation string ("wrote N bytes to X" —
// or a memory delete's "Removed or already absent: X"); an empty output
// renders nothing.
export function WriteFileBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <div>{output}</div>;
}
