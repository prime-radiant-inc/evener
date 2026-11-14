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

const hit = (key: string): FindHit => ({ key, itemId: key, rowIndex: 0 });
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

test("a phrase is painted across the nodes it spans, skipping what is hidden from readers", () => {
  // A code block renders each line as its own element, with a line number
  // between them that is not part of the text.
  const el = port({ a: "" });
  const anchor = el.firstElementChild as HTMLElement;
  anchor.innerHTML =
    '<span style="display:flex"><span>find the needle</span></span>' +
    '<span style="display:flex"><span aria-hidden="true">2</span><span>thread here</span></span>';
  markTranscript(el, [hit("a")], "a", "needle thread");
  const ranges = registry.get("evener-find-current")?.ranges ?? [];
  expect(ranges).toHaveLength(1);
  expect(ranges[0]?.startContainer.nodeValue).toBe("find the needle");
  expect(ranges[0]?.startOffset).toBe("find the ".length);
  expect(ranges[0]?.endContainer.nodeValue).toBe("thread here");
  expect(ranges[0]?.endOffset).toBe("thread".length);
  // The line number itself is never a match.
  markTranscript(el, [hit("a")], "a", "2");
  expect(registry.has("evener-find-current")).toBe(false);
});

test("words run together across inline formatting are one phrase; separate blocks are separate words", () => {
  // Coloured output puts each colour run in its own element with nothing
  // between them.
  const el = port({ a: "" });
  const anchor = el.firstElementChild as HTMLElement;
  anchor.innerHTML = "<p><span>error</span>: build <b>fail</b><i>ed</i></p><p>next</p>";
  markTranscript(el, [hit("a")], "a", "error: build failed");
  const ranges = registry.get("evener-find-current")?.ranges ?? [];
  expect(ranges).toHaveLength(1);
  expect(ranges[0]?.startContainer.nodeValue).toBe("error");
  expect(ranges[0]?.endContainer.nodeValue).toBe("ed");
  // Two paragraphs never read as one word, and do read as two.
  markTranscript(el, [hit("a")], "a", "failednext");
  expect(registry.has("evener-find-current")).toBe(false);
  markTranscript(el, [hit("a")], "a", "failed next");
  expect(painted("evener-find-current")).toHaveLength(1);
});

test("a line break inside one element separates words", () => {
  const el = port({ a: "" });
  (el.firstElementChild as HTMLElement).innerHTML = "<p>first<br>second</p>";
  markTranscript(el, [hit("a")], "a", "first second");
  expect(painted("evener-find-current")).toHaveLength(1);
});

test("two transcripts keep their own painted words", () => {
  const left = port({ a: "left needle" });
  const right = port({ b: "right needle" });
  markTranscript(left, [hit("a")], "a", "needle");
  markTranscript(right, [hit("b")], "b", "needle");
  expect(painted("evener-find-current")).toHaveLength(2);
  // Closing Find in one leaves the other's words painted.
  markTranscript(right, [], null, "");
  expect(painted("evener-find-current")).toEqual(["needle"]);
  markTranscript(left, [], null, "");
  expect(registry.size).toBe(0);
});

test("a transcript in another window paints in that window", () => {
  // A popped-out session renders into its own document, whose highlight
  // registry is the only one that paints there.
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  const view = frame.contentWindow as Window & { CSS?: unknown; Highlight?: unknown };
  const popout = new Map<string, FakeHighlight>();
  view.CSS = { highlights: popout };
  view.Highlight = FakeHighlight;
  const el = view.document.createElement("div");
  el.innerHTML = '<div data-view-anchor-id="a" data-view-anchor-index="0"><p>one <b>need</b>le here</p></div>';
  view.document.body.appendChild(el);

  const marks = markTranscript(el, [hit("a")], "a", "needle");
  expect(marks.current?.getAttribute("data-view-anchor-id")).toBe("a");
  expect((popout.get("evener-find-current")?.ranges ?? []).map((range) => range.toString())).toEqual(["needle"]);
  expect(registry.size).toBe(0);
  markTranscript(el, [], null, "");
  expect(popout.size).toBe(0);
});
