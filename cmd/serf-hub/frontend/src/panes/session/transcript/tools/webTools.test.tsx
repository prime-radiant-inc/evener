import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./webTools";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- web_fetch ----------------------------------------------------------
// Ground truth (verified against agent/tool_web_fetch.go:172-183 and
// agent/internal/tool/registry.go's toolValueToString): web_fetch's Exec
// returns a plain map[string]any{answer, raw_file, url, content_type,
// size_bytes, markdown_file?} - NOT a StateResult, so it falls through the
// registry's default branch, which json.MarshalIndent()s any non-string/
// []byte return value. item.output is therefore genuine, reliably
// JSON.parse-able JSON (unlike the job_list/job_stop/shell family, which
// return human-formatted text) - this descriptor parses it directly rather
// than treating it as an opaque line-oriented preview.

test("web_fetch: summary shows size_bytes from the parsed JSON output, not the raw text length", () => {
  const d = toolRendererFor("web_fetch");
  const args = JSON.stringify({ url: "https://example.com", question: "what is this?" });
  const output = JSON.stringify({ answer: "It's an example.", url: "https://example.com", size_bytes: 4096 });
  expect(d.summary(item({ toolName: "web_fetch", argumentsJSON: args, output }))).toBe(
    "Fetched https://example.com · 4096 bytes",
  );
});

test("web_fetch: summary falls back to the raw text length when output isn't parseable JSON", () => {
  const d = toolRendererFor("web_fetch");
  const args = JSON.stringify({ url: "https://example.com", question: "q" });
  expect(d.summary(item({ toolName: "web_fetch", argumentsJSON: args, output: "not json" }))).toBe(
    "Fetched https://example.com · 8 bytes",
  );
});

test("web_fetch: body shows the extracted `answer` field, not the raw JSON envelope", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  const output = JSON.stringify({ answer: "The page says hello.", url: "https://example.com", size_bytes: 10 });
  render(<Body item={item({ toolName: "web_fetch", output })} live={false} />);
  expect(screen.getByText("The page says hello.")).toBeTruthy();
  expect(screen.queryByText(/"answer"/)).toBeNull();
});

test("web_fetch: body falls back to the raw output text when it isn't parseable JSON", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  render(<Body item={item({ toolName: "web_fetch", output: "plain fallback text" })} live={false} />);
  expect(screen.getByText("plain fallback text")).toBeTruthy();
});

test("web_fetch: renders nothing when output is blank", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  const { container } = render(<Body item={item({ toolName: "web_fetch", output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

// --- web_search -----------------------------------------------------------

test("web_search: summary shows the clipped query and a result count", () => {
  const d = toolRendererFor("web_search");
  const args = JSON.stringify({ query: "serf webui rewrite" });
  const output = "result one\nresult two\n";
  expect(d.summary(item({ toolName: "web_search", argumentsJSON: args, output }))).toBe(
    'Searched the web for "serf webui rewrite" · 2 results',
  );
});

test("web_search: falls back to the `q` arg key when `query` is absent", () => {
  const d = toolRendererFor("web_search");
  const args = JSON.stringify({ q: "fallback query" });
  expect(d.summary(item({ toolName: "web_search", argumentsJSON: args, output: "" }))).toBe(
    'Searched the web for "fallback query" · 0 results',
  );
});

test("web_search: a long query is clipped to 120 chars", () => {
  const d = toolRendererFor("web_search");
  const longQuery = "q".repeat(130);
  const args = JSON.stringify({ query: longQuery });
  const summary = d.summary(item({ toolName: "web_search", argumentsJSON: args, output: "" }));
  expect(summary.startsWith(`Searched the web for "${"q".repeat(120)}…"`)).toBe(true);
});

test("web_fetch: the url reads straight from a settled item's own argumentsJSON (the model preserves it through item/completed - see R2)", () => {
  const d = toolRendererFor("web_fetch");
  const args = JSON.stringify({ url: "https://settled.example", question: "q" });
  const settled = item({
    toolName: "web_fetch",
    argumentsJSON: args,
    output: JSON.stringify({ answer: "hi", size_bytes: 2 }),
  });
  expect(d.summary(settled)).toBe("Fetched https://settled.example · 2 bytes");
});

test("web_search: body lists up to 5 trimmed result lines", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  const output = ["  one  ", "two", "three", "four", "five", "six (dropped)"].join("\n");
  render(<Body item={item({ toolName: "web_search", output })} live={false} />);
  expect(screen.getByText("one")).toBeTruthy();
  expect(screen.getByText("five")).toBeTruthy();
  expect(screen.queryByText("six (dropped)")).toBeNull();
});

// kata tcp9: the fetched URL must be openable in the user's own browser —
// a real anchor, new tab, opener severed. The URL comes from argumentsJSON
// (the call's own input, present even when the output isn't JSON or the
// fetch failed), not from the output envelope.
test("kata tcp9: body renders the fetched URL as a link that opens in the user's browser", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  const args = JSON.stringify({ url: "https://example.com/page", question: "q" });
  const output = JSON.stringify({ answer: "Hello.", url: "https://example.com/page", size_bytes: 6 });
  render(<Body item={item({ toolName: "web_fetch", argumentsJSON: args, output })} live={false} />);
  const link = screen.getByRole("link", { name: "https://example.com/page" }) as HTMLAnchorElement;
  expect(link.getAttribute("href")).toBe("https://example.com/page");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
});

test("kata tcp9: a failed fetch's plain-text body still links the URL the call was for", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  const args = JSON.stringify({ url: "https://example.com/down", question: "q" });
  render(
    <Body item={item({ toolName: "web_fetch", argumentsJSON: args, output: "fetch failed: timeout" })} live={false} />,
  );
  expect((screen.getByRole("link") as HTMLAnchorElement).getAttribute("href")).toBe("https://example.com/down");
  expect(screen.getByText("fetch failed: timeout")).toBeTruthy();
});

test("kata tcp9: no url argument means no link, never a dead anchor", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  render(<Body item={item({ toolName: "web_fetch", argumentsJSON: "not json", output: "text" })} live={false} />);
  expect(screen.queryByRole("link")).toBeNull();
});

// kata xw3t: the collapsed row's own "Fetched <url> · N bytes" summary was
// the surface tcp9 deliberately left inert. The descriptor's summaryLink
// must return the SAME URL, under the SAME http(s)-only rule, as the
// expanded body's own link above - never a second, independently-drifting
// source of truth for "what does this call's own link point to".
test("kata xw3t: web_fetch's descriptor exposes the fetched url as summaryLink, for the collapsed row", () => {
  const d = toolRendererFor("web_fetch");
  const args = JSON.stringify({ url: "https://example.com/page", question: "q" });
  expect(d.summaryLink?.(item({ toolName: "web_fetch", argumentsJSON: args }))).toBe("https://example.com/page");
});

test("kata xw3t: web_fetch's summaryLink is undefined for a non-http(s) or missing url, same rule as the body's own link", () => {
  const d = toolRendererFor("web_fetch");
  expect(d.summaryLink?.(item({ toolName: "web_fetch", argumentsJSON: "not json" }))).toBeUndefined();
  const jsArgs = JSON.stringify({ url: "javascript:alert(1)" });
  expect(d.summaryLink?.(item({ toolName: "web_fetch", argumentsJSON: jsArgs }))).toBeUndefined();
});

// kata xw3t: web_search's result lines are free-form prose (agent/
// tool_web_search.go's webSearch returns the grounded model's own
// resp.Text() - no structured URL field like web_fetch's own
// argumentsJSON.url), so any URL is wherever the model's own text put it -
// this scans the rendered line itself, same http(s)-only/target/rel idiom.
test("kata xw3t: web_search body linkifies a bare URL inside a result line", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  const output = "See https://example.com/article for details";
  render(<Body item={item({ toolName: "web_search", output })} live={false} />);
  const link = screen.getByRole("link", { name: "https://example.com/article" }) as HTMLAnchorElement;
  expect(link.getAttribute("href")).toBe("https://example.com/article");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  // No text lost or duplicated splitting the line around the link.
  expect(link.closest("li")?.textContent).toBe(output);
});

test("kata xw3t: web_search body linkifies more than one URL on the same line", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  const output = "Compare https://a.example/one and https://b.example/two";
  render(<Body item={item({ toolName: "web_search", output })} live={false} />);
  expect(screen.getByRole("link", { name: "https://a.example/one" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "https://b.example/two" })).toBeTruthy();
});

test("kata xw3t: a URL ending a sentence does not pull the period into the href", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  const output = "Read more at https://example.com/article.";
  render(<Body item={item({ toolName: "web_search", output })} live={false} />);
  const link = screen.getByRole("link") as HTMLAnchorElement;
  expect(link.getAttribute("href")).toBe("https://example.com/article");
  expect(link.textContent).toBe("https://example.com/article");
  // The trailing period is still on screen, just outside the anchor.
  expect(link.closest("li")?.textContent).toBe(output);
});

// clip() (helpers.ts, RESULT_LINE_CLIP=200) cuts on a raw character budget
// with no notion of "mid-URL" and appends its own "…". A match touching
// that boundary may not be the real URL at all - linkifying it anyway would
// be exactly the dead-or-wrong anchor tcp9's own carried-over rule forbids.
test("kata xw3t: a URL right at the 200-char clip boundary is never linkified - a truncated href would be a dead anchor", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  const longUrl = `https://example.com/${"a".repeat(200)}`;
  render(<Body item={item({ toolName: "web_search", output: longUrl })} live={false} />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByText(`${longUrl.slice(0, 200)}…`)).toBeTruthy();
});

test("kata xw3t: a line with no URL is unaffected - no accidental link", () => {
  const d = toolRendererFor("web_search");
  const Body = d.body!;
  render(<Body item={item({ toolName: "web_search", output: "no links here" })} live={false} />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByText("no links here")).toBeTruthy();
});
