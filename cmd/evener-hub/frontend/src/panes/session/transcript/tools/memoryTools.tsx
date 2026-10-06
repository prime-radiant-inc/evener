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
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { EditFileBody, HeadClippedOutputBody, TailFoldedOutputBody, WriteFileBody } from "./bodies";

const MEMORY_CONTENT_MAX_CHARS = 8000;

function MemoryWriteBody({ item, live }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const content = str(args, "content");
  if (content !== undefined) return <Markdown source={clip(content, MEMORY_CONTENT_MAX_CHARS)} />;

  return <WriteFileBody item={item} live={live} />;
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
  body: EditFileBody,
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
