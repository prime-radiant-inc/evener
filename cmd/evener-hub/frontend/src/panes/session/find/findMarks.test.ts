// jsdom has no CSS Custom Highlight API, so these tests supply the two
// globals a supporting browser has and read back what Find registers.
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { FindHit } from "./findHits";
import { markTranscript } from "./findMarks";

class FakeHighlight {
  readonly ranges: Range[];
  constructor(...ranges: Range[]) {
    this.ranges = ranges;
  }
}

let registry: Map<string, FakeHighlight>;

beforeEach(() => {
  registry = new Map();
  vi.stubGlobal("Highlight", FakeHighlight);
  vi.stubGlobal("CSS", { highlights: registry });
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

function port(entries: Record<string, string>): HTMLElement {
  const el = document.createElement("div");
  for (const [key, text] of Object.entries(entries)) {
    const anchor = document.createElement("div");
    anchor.setAttribute("data-view-anchor-id", key);
    anchor.setAttribute("data-view-anchor-index", "0");
    anchor.textContent = text;
    el.appendChild(anchor);
  }
  document.body.appendChild(el);
  return el;
}

const hit = (key: string): FindHit => ({ key, itemId: key, rowIndex: 0, opens: [] });
const painted = (name: string) => (registry.get(name)?.ranges ?? []).map((range) => range.toString());

test("the current entry's words and the other matches are painted separately", () => {
  const el = port({ a: "a Needle and a needle", b: "one needle" });
  markTranscript(el, [hit("a"), hit("b")], "b", "needle");
  expect(painted("evener-find")).toEqual(["Needle", "needle"]);
  expect(painted("evener-find-current")).toEqual(["needle"]);
  markTranscript(el, [], null, "");
  expect(registry.size).toBe(0);
});

test("a letter whose lowercase form is longer does not shift or break the painted words", () => {
  // "İ" lowercases to two code units, so offsets in the lowercased text do
  // not address the same characters in the text on screen.
  const el = port({ a: "İx and İstanbul x" });
  expect(() => markTranscript(el, [hit("a")], "a", "x")).not.toThrow();
  expect(painted("evener-find-current")).toEqual(["x", "x"]);
  markTranscript(el, [hit("a")], "a", "stanbul");
  expect(painted("evener-find-current")).toEqual(["stanbul"]);
});
