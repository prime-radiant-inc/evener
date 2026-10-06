import {
  clip,
  editDiffText,
  memoryDeleteSummary,
  memoryEditSummary,
  memoryReadSummary,
  memorySearchSummary,
  memoryWriteSummary,
  parseArgs,
  str,
} from "@evener/appwire-client";
import { DiffBlock, Markdown } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";

const MEMORY_CONTENT_MAX_CHARS = 8000;

function MemoryWriteBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const content = str(args, "content");
  if (content !== undefined) return <Markdown source={clip(content, MEMORY_CONTENT_MAX_CHARS)} />;

  const output = item.output ?? "";
  if (output === "") return null;
  return <div>{output}</div>;
}

function MemoryEditBody({ item }: ToolRenderProps) {
  const args = parseArgs(item.argumentsJSON);
  const path = str(args, "file_path") ?? str(args, "path") ?? "";
  const oldString = str(args, "old_string") ?? "";
  const newString = str(args, "new_string") ?? "";
  return <DiffBlock unified={editDiffText(path, oldString, newString)} />;
}

function MemoryDeleteBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  return <div>{output}</div>;
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
  body: MemoryDeleteBody,
});
