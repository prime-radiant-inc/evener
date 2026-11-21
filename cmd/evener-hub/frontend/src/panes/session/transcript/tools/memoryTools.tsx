import {
  clip,
  editOperationsOf,
  filePathOf,
  memoryDeleteSummary,
  memoryEditSummary,
  memoryReadSummary,
  memorySearchSummary,
  memoryWritePage,
  memoryWriteSummary,
  parseArgs,
} from "@evener/appwire-client";
import { Markdown } from "../../../../widgets";
import { defaultToolBody, registerToolRenderer, type ToolRenderProps } from "../toolRenderers";
import {
  EditDiffBody,
  HeadClippedOutputBody,
  TailFoldedOutputBody,
  TOOL_BODY_MAX_CHARS,
  WriteFileBody,
} from "./bodies";

function MemoryWriteBody({ item, live }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  // Only the current argument shape renders; a fallback for the older `content`
  // argument is a compatibility decision that needs Jesse's approval.
  const page = memoryWritePage(args);
  // An empty page renders the confirmation, not an empty markdown block -
  // the same empty-body guard the native evidence applies.
  if (page) return <Markdown source={clip(page, TOOL_BODY_MAX_CHARS)} />;

  return <WriteFileBody item={item} live={live} />;
}

// A memory_edit with no valid edit operations is a malformed call: mirror the
// native evidence's fallback to the generic arguments/result display rather
// than an empty diff.
function MemoryEditBody(props: ToolRenderProps) {
  const args = parseArgs(props.item.argumentsJSON);
  const ops = editOperationsOf(args);
  if (ops === null) return defaultToolBody(props);
  return <EditDiffBody path={filePathOf(args) ?? ""} ops={ops} />;
}

registerToolRenderer({
  match: "memory_write",
  icon: "memory",
  fold: "consequential",
  summary: memoryWriteSummary,
  body: MemoryWriteBody,
});

registerToolRenderer({
  match: "memory_edit",
  icon: "memory",
  fold: "consequential",
  summary: memoryEditSummary,
  body: MemoryEditBody,
});

registerToolRenderer({
  match: "memory_read",
  icon: "memory",
  fold: "quiet",
  summary: memoryReadSummary,
  body: TailFoldedOutputBody,
});

registerToolRenderer({
  match: "memory_search",
  icon: "memory",
  fold: "quiet",
  summary: memorySearchSummary,
  body: HeadClippedOutputBody,
});

registerToolRenderer({
  match: "memory_delete",
  icon: "memory",
  fold: "consequential",
  summary: memoryDeleteSummary,
  body: WriteFileBody,
});
