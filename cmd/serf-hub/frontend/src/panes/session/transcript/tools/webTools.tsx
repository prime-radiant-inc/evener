// Descriptors for web_fetch and web_search (parity checklist §2).
//
// Ground truth on web_fetch specifically (verified against
// agent/tool_web_fetch.go:172-183 and agent/internal/tool/registry.go's
// toolValueToString): its Exec returns a plain
// map[string]any{answer,raw_file,url,content_type,size_bytes,
// markdown_file?}, which - not being a StateResult - falls through the
// registry's default json.MarshalIndent branch. item.output is therefore
// real, reliably JSON.parse-able JSON, unlike every other tool in this
// directory (job_list/job_stop/shell/... all return human-formatted
// text) - this descriptor parses it directly for an accurate byte count
// and the model's own extracted answer, with a defensive plain-text
// fallback if a future/older payload isn't JSON after all.
//
// web_search has no such structured output (a bare prose answer string on
// the one path - Gemini - where this tool is even registered as a
// function-tool at all; OpenAI/Anthropic web search is a provider-native
// server tool that never becomes a live commandExecution item) - its body
// stays a short line-oriented preview, matching the legacy
// webSearchRenderer's own "don't dump the whole page inline" restraint.

import type { ItemModel } from "../../../../protocol/model";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, formatByteCount, parseArgs, parseJSONObject, str } from "./helpers";

const QUERY_CLIP = 120;
const RESULT_LINE_CLIP = 200;

function nonBlankLines(text: string): string[] {
  return text.split("\n").filter((line) => line.trim() !== "");
}

// webFetchByteCount prefers the JSON envelope's own size_bytes (the
// fetched page's real size) over the output text's own length (which
// would instead measure the pretty-printed JSON wrapper).
function webFetchByteCount(output: string): number {
  const parsed = parseJSONObject(output);
  const sizeBytes = parsed?.["size_bytes"];
  return typeof sizeBytes === "number" ? sizeBytes : output.length;
}

function WebFetchBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const parsed = parseJSONObject(output);
  const answer = parsed ? str(parsed, "answer") : undefined;
  return <div>{clip(answer ?? output, 240)}</div>;
}

registerToolRenderer({
  match: "web_fetch",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const url = str(args, "url") ?? "";
    return `Fetched ${url} · ${formatByteCount(webFetchByteCount(item.output ?? ""))}`;
  },
  body: WebFetchBody,
});

function WebSearchBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const lines = nonBlankLines(output)
    .slice(0, 5)
    .map((line) => clip(line.trim(), RESULT_LINE_CLIP));
  return (
    <ul>
      {lines.map((line, i) => (
        <li key={i}>{line}</li>
      ))}
    </ul>
  );
}

registerToolRenderer({
  match: "web_search",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const query = clip(str(args, "query") ?? str(args, "q") ?? "", QUERY_CLIP);
    const resultCount = nonBlankLines(item.output ?? "").length;
    return `Searched the web for "${query}" · ${resultCount} results`;
  },
  body: WebSearchBody,
});
