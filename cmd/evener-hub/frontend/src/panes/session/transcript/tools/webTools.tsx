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

import type { ReactNode } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { ContextCard } from "../../../../widgets/contextcard";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, formatByteCount, parseArgs, parseJSONObject, str } from "./helpers";

const QUERY_CLIP = 120;
const RESULT_LINE_CLIP = 200;

function nonBlankLines(text: string): string[] {
  return text.split("\n").filter((line) => line.trim() !== "");
}

// A conservative bare-URL matcher for web_search's free-form result text
// (kata xw3t): unlike web_fetch, this tool has no structured URL field to
// read (agent/tool_web_search.go's webSearch returns the grounded model's
// own resp.Text(), plain prose - see that file), so any URL is wherever the
// model's own text happens to put it. Stops at whitespace and a small set
// of characters a bare URL a human is reading is unlikely to end in itself
// (quotes, angle brackets, clip()'s own ellipsis below).
const BARE_URL_PATTERN = /https?:\/\/[^\s<>"'…]+/g;

// Trims sentence punctuation trailing a matched URL - the period ending the
// SENTENCE, not the URL - off both the href and the visible label, matching
// common autolink convention. Falls back to the untrimmed match if
// stripping would leave no host/path character at all (a pathological
// "https://..." run), rather than link to a bare scheme.
const URL_TRAILING_PUNCTUATION = /[.,;:!?)\]}]+$/;
function stripUrlTrailingPunctuation(url: string): string {
  const stripped = url.replace(URL_TRAILING_PUNCTUATION, "");
  return /^https?:\/\/./.test(stripped) ? stripped : url;
}

// linkifyLine turns bare http(s) URLs inside one line of free text into
// real links, same target/rel idiom as tcp9's web_fetch link. A match
// touching the very end of a line clip() (below) has truncated - the line
// ends in clip's own "…" - is left as plain text instead: clip() cuts on a
// raw character budget with no notion of "mid-URL", so the tail of that
// match may not be the real URL at all, and tcp9's "never a dead anchor"
// rule carries over here too.
function linkifyLine(line: string): ReactNode {
  const truncated = line.endsWith("…");
  const matches = [...line.matchAll(BARE_URL_PATTERN)].filter((m) => {
    const end = (m.index ?? 0) + m[0].length;
    return !(truncated && end === line.length - 1);
  });
  if (matches.length === 0) return line;
  const nodes: ReactNode[] = [];
  let cursor = 0;
  for (const match of matches) {
    const start = match.index ?? 0;
    const raw = match[0];
    const href = stripUrlTrailingPunctuation(raw);
    nodes.push(line.slice(cursor, start));
    nodes.push(
      <a key={start} href={href} target="_blank" rel="noopener noreferrer">
        {href}
      </a>,
    );
    nodes.push(raw.slice(href.length)); // trailing punctuation stripped from the href, kept as plain text
    cursor = start + raw.length;
  }
  nodes.push(line.slice(cursor));
  return nodes;
}

// webFetchByteCount prefers the JSON envelope's own size_bytes (the
// fetched page's real size) over the output text's own length (which
// would instead measure the pretty-printed JSON wrapper).
function webFetchByteCount(output: string): number {
  const parsed = parseJSONObject(output);
  const sizeBytes = parsed?.size_bytes;
  return typeof sizeBytes === "number" ? sizeBytes : output.length;
}

// The fetched URL, linkified so the reader can open it in their own browser
// (kata tcp9). Sourced from argumentsJSON — the call's own input, present
// even when the fetch failed and the output is bare error text — never from
// the output envelope. http(s) only: an anchor built from tool-call text
// must not be able to carry a javascript:/data: href, so anything else
// renders as no link at all rather than a dead or dangerous one. Same
// target/rel idiom as the markdown widget's own external links.
function webFetchLink(item: { argumentsJSON?: string }): string | undefined {
  const url = str(parseArgs(item.argumentsJSON), "url");
  if (url === undefined) return undefined;
  return url.startsWith("https://") || url.startsWith("http://") ? url : undefined;
}

// The body is a ContextCard: the fetched URL as the source line, the
// model's extracted answer as the snippet, the page's real size as the
// meta caption. The card's href goes through webFetchLink's http(s)-only
// rule (tcp9), so a non-web url still shows as source *text* but the card
// never becomes a link for it.
function WebFetchBody({ item }: ToolRenderProps) {
  const output = item.output ?? "";
  if (output === "") return null;
  const parsed = parseJSONObject(output);
  const answer = parsed ? str(parsed, "answer") : undefined;
  return (
    <ContextCard
      source={str(parseArgs(item.argumentsJSON), "url") ?? ""}
      snippet={clip(answer ?? output, 240)}
      meta={formatByteCount(webFetchByteCount(output))}
      href={webFetchLink(item)}
    />
  );
}

registerToolRenderer({
  match: "web_fetch",
  icon: "globe",
  fold: "quiet",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const url = str(args, "url") ?? "";
    return `Fetched ${url} · ${formatByteCount(webFetchByteCount(item.output ?? ""))}`;
  },
  // kata xw3t: the collapsed row's own "Fetched <url> · N bytes" line reuses
  // the exact same http(s)-only URL webFetchLink already computes for the
  // expanded body (tcp9) - same source (argumentsJSON, not the output
  // envelope), same validation, so the collapsed and expanded surfaces can
  // never disagree about which URL is safe to link.
  summaryLink: webFetchLink,
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
        // lines is derived fresh each render from item.output, a completed
        // tool call's fixed, immutable result string - same slice every
        // time, never reordered; two lines can also legitimately share
        // identical clipped text, which a content-based key would collide on.
        // biome-ignore lint/suspicious/noArrayIndexKey: fixed source text, possible duplicate lines, see above
        <li key={i}>{linkifyLine(line)}</li>
      ))}
    </ul>
  );
}

registerToolRenderer({
  match: "web_search",
  icon: "search",
  fold: "quiet",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const query = clip(str(args, "query") ?? str(args, "q") ?? "", QUERY_CLIP);
    const resultCount = nonBlankLines(item.output ?? "").length;
    return `Searched the web for "${query}" · ${resultCount} results`;
  },
  body: WebSearchBody,
});
