// Descriptors for web_fetch and web_search (parity checklist §2). Neither
// tool streams its body live in legacy (bodyEnd-only) - both previews here
// are similarly settled-only, built from a short excerpt of the output
// rather than the full text, matching the legacy "don't overwhelm with a
// full page dump" intent.
import { registerToolRenderer } from "../toolRenderers";
import type { ToolRenderProps } from "../toolRenderers";
import { clip, formatByteCount, rememberedArgs, str } from "./helpers";
import type { ItemModel } from "../../../../protocol/model";

const QUERY_CLIP = 120;
const RESULT_LINE_CLIP = 200;

function nonBlankLines(text: string): string[] {
  return text.split("\n").filter((line) => line.trim() !== "");
}

function WebFetchBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const preview = nonBlankLines(output).slice(0, 3).join(" / ");
  return <div>{clip(preview, 240)}</div>;
}

registerToolRenderer({
  match: "web_fetch",
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    const url = str(args, "url") ?? "";
    return `Fetched ${url} · ${formatByteCount((item.output ?? "").length)}`;
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
    const args = rememberedArgs(item);
    const query = clip(str(args, "query") ?? str(args, "q") ?? "", QUERY_CLIP);
    const resultCount = nonBlankLines(item.output ?? "").length;
    return `Searched the web for "${query}" · ${resultCount} results`;
  },
  body: WebSearchBody,
});
