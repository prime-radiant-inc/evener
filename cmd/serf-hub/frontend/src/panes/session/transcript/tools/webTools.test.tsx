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
  render(<Body item={item({ toolName: "web_fetch", argumentsJSON: args, output: "fetch failed: timeout" })} live={false} />);
  expect((screen.getByRole("link") as HTMLAnchorElement).getAttribute("href")).toBe("https://example.com/down");
  expect(screen.getByText("fetch failed: timeout")).toBeTruthy();
});

test("kata tcp9: no url argument means no link, never a dead anchor", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  render(<Body item={item({ toolName: "web_fetch", argumentsJSON: "not json", output: "text" })} live={false} />);
  expect(screen.queryByRole("link")).toBeNull();
});
