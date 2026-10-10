import {
  clip,
  memoryDeleteSummary,
  memoryEditSummary,
  memoryReadSummary,
  memorySearchSummary,
  memoryWriteSummary,
  parseArgs,
  str,
} from "@evener/appwire-client";
import { Markdown } from "../../../../widgets";
import { defaultToolBody, registerToolRenderer, type ToolRenderProps } from "../toolRenderers";
import { EditFileBody, HeadClippedOutputBody, TailFoldedOutputBody, WriteFileBody } from "./bodies";

const MEMORY_CONTENT_MAX_CHARS = 8000;

function MemoryWriteBody({ item, live }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const content = str(args, "content");
  // An empty page renders the confirmation, not an empty markdown block -
  // the same empty-content guard the native evidence applies.
  if (content) return <Markdown source={clip(content, MEMORY_CONTENT_MAX_CHARS)} />;

  return <WriteFileBody item={item} live={live} />;
}

// A memory_edit with neither old nor new string is a malformed call: mirror
// the native evidence's fallback to the generic arguments/result display
// rather than an empty diff. edit_file keeps its own always-render shape,
// which predates this sharing.
function MemoryEditBody(props: ToolRenderProps) {
  const args = parseArgs(props.item.argumentsJSON);
  const oldString = str(args, "old_string");
  const newString = str(args, "new_string");
  if (oldString === undefined && newString === undefined) return defaultToolBody(props);
  return <EditFileBody {...props} />;
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
