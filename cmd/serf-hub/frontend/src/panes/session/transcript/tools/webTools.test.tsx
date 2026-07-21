import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./webTools";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- web_fetch ----------------------------------------------------------

test("web_fetch: summary shows the byte count of a successful fetch", () => {
  const d = toolRendererFor("web_fetch");
  const args = JSON.stringify({ url: "https://example.com", question: "what is this?" });
  expect(d.summary(item({ toolName: "web_fetch", argumentsJSON: args, output: "hello world" }))).toBe(
    "Fetched https://example.com · 11 bytes",
  );
});

test("web_fetch: body previews up to the first 3 non-blank lines, joined and clipped", () => {
  const d = toolRendererFor("web_fetch");
  const Body = d.body!;
  const output = "first line\n\nsecond line\nthird line\nfourth line (dropped)";
  render(<Body item={item({ toolName: "web_fetch", output })} live={false} />);
  expect(screen.getByText("first line / second line / third line")).toBeTruthy();
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

test("web_fetch: the url survives once the item settles and argumentsJSON goes missing, via rememberedArgs", () => {
  const d = toolRendererFor("web_fetch");
  const callId = "web_settle_1";
  const args = JSON.stringify({ url: "https://settled.example", question: "q" });
  d.summary(item({ toolName: "web_fetch", callId, argumentsJSON: args }));
  const settled = item({ toolName: "web_fetch", callId, argumentsJSON: undefined, output: "hi" });
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
