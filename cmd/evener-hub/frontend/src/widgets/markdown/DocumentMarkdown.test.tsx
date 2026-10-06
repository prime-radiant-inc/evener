import { render, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { DocumentMarkdown } from "./DocumentMarkdown";
import { type BoundDocumentMap, bindDocumentMap, renderDocument } from "./documentSourceMap";
import { Markdown } from "./index";

// Replace only Mermaid's external SVG generator. Its real security sanitizer,
// React lifecycle, source fallback and document DOM mapping remain in the test.
vi.mock("mermaid", () => ({
  default: {
    initialize: () => {},
    render: async (_id: string, source: string) => {
      if (source.startsWith("broken")) throw new Error("external diagram syntax error");
      return { svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>generated same</text><script>bad()</script></svg>' };
    },
  },
}));
afterEach(() => window.getSelection()?.removeAllRanges());
function selectionFor(node: Node, start?: number, end?: number) {
  const r = document.createRange();
  if (start === undefined || end === undefined) r.selectNodeContents(node);
  else {
    r.setStart(node, start);
    r.setEnd(node, end);
  }
  const s = window.getSelection();
  if (!s) throw new Error("selection unavailable");
  s.removeAllRanges();
  s.addRange(r);
  return s;
}
function mapped(source: string) {
  let bound: BoundDocumentMap | null = null;
  const view = render(
    <DocumentMarkdown
      source={source}
      identity={{ displayedSnapshotId: "s1", mappingId: "m1" }}
      onMap={(value) => {
        bound = value;
      }}
    />,
  );
  if (!bound) throw new Error("document not bound");
  return { ...view, map: bound as BoundDocumentMap };
}
// A mapper which searches for the first duplicate must fail this literal.
test("maps the second duplicate, including element endpoints", () => {
  const { container, map } = mapped("same same.");
  const p = container.querySelector("p");
  if (!p?.firstChild) throw new Error("no paragraph text");
  expect(map.capture(selectionFor(p.firstChild, 5, 9))).toEqual({
    state: "mapped",
    sourceRange: { start: 5, end: 9 },
    selectedRenderedText: "same",
  });
  expect(map.capture(selectionFor(p))).toMatchObject({ state: "mapped", sourceRange: { start: 0, end: 10 } });
});
test("backwards Selection has the same document-ordered endpoints", () => {
  const { container, map } = mapped("same same.");
  const node = container.querySelector("p")?.firstChild;
  if (!node) throw new Error("no text");
  const s = window.getSelection();
  if (!s) throw new Error("no selection");
  s.setBaseAndExtent(node, 9, node, 5);
  expect(s.anchorOffset).toBe(9);
  expect(s.focusOffset).toBe(5);
  expect(map.capture(s)).toEqual({ state: "mapped", sourceRange: { start: 5, end: 9 }, selectedRenderedText: "same" });
});
test.each([
  ["remove", "element"],
  ["remove", "text"],
  ["empty", "element"],
  ["empty", "text"],
  ["replace data", "element"],
  ["replace data", "text"],
  ["replace node", "element"],
  ["replace node", "text"],
] as const)("rejects %s of an interior run with %s endpoints", (mutation, endpoints) => {
  const { container, map } = mapped("first **middle** last");
  const paragraph = container.querySelector("p");
  const strong = paragraph?.querySelector("strong");
  const first = paragraph?.firstChild;
  const middle = strong?.firstChild;
  const last = paragraph?.lastChild;
  if (!(first instanceof Text) || !(middle instanceof Text) || !(last instanceof Text) || !paragraph || !strong)
    throw new Error("missing authored runs");
  function passage() {
    if (endpoints === "element") return selectionFor(paragraph as HTMLParagraphElement);
    const range = document.createRange();
    range.setStart(first as Text, 0);
    range.setEnd(last as Text, 5);
    const selection = window.getSelection();
    if (!selection) throw new Error("selection unavailable");
    selection.removeAllRanges();
    selection.addRange(range);
    return selection;
  }
  expect(map.capture(passage())).toEqual({
    state: "mapped",
    sourceRange: { start: 0, end: 21 },
    selectedRenderedText: "first middle last",
  });
  expect(
    map
      .rangesFor({ start: 0, end: 21 })
      .map((range) => range.toString())
      .join(""),
  ).toBe("first middle last");
  if (mutation === "remove") strong.remove();
  else if (mutation === "empty") middle.data = "";
  else if (mutation === "replace data") middle.data = "xxxxxx";
  else strong.replaceChildren(document.createTextNode("middle"));
  expect(map.capture(passage()).state).toBe("unmapped");
  expect(map.capture(selectionFor(first, 0, 5))).toEqual({
    state: "mapped",
    sourceRange: { start: 0, end: 5 },
    selectedRenderedText: "first",
  });
  expect(map.capture(selectionFor(last, 1, 5))).toEqual({
    state: "mapped",
    sourceRange: { start: 17, end: 21 },
    selectedRenderedText: "last",
  });
  expect(
    map
      .rangesFor({ start: 0, end: 5 })
      .map((range) => range.toString())
      .join(""),
  ).toBe("first");
  expect(
    map
      .rangesFor({ start: 17, end: 21 })
      .map((range) => range.toString())
      .join(""),
  ).toBe("last");
});
test("identity and source replacement invalidate old maps even with identical rendered text", () => {
  let bound: BoundDocumentMap | null = null;
  const onMap = (value: BoundDocumentMap | null) => {
    bound = value;
  };
  const view = render(
    <DocumentMarkdown source="same" identity={{ displayedSnapshotId: "s", mappingId: "m" }} onMap={onMap} />,
  );
  if (!bound) throw new Error("no first map");
  const old = bound as BoundDocumentMap;
  view.rerender(
    <DocumentMarkdown source={"\ufeffsame"} identity={{ displayedSnapshotId: "s2", mappingId: "m2" }} onMap={onMap} />,
  );
  if (!bound) throw new Error("no replacement map");
  const p = view.container.querySelector("p");
  if (!p) throw new Error("no paragraph");
  const s = selectionFor(p);
  expect(old.capture(s)).toEqual({ state: "unmapped", reason: "stale" });
  expect((bound as BoundDocumentMap).capture(s)).toMatchObject({ state: "mapped", sourceRange: { start: 1, end: 5 } });
  view.unmount();
  expect(bound).toBeNull();
});
test("changing the map callback does not lose verified DOM runs", () => {
  const first = vi.fn();
  let second: BoundDocumentMap | null = null;
  const view = render(
    <DocumentMarkdown source="same" identity={{ displayedSnapshotId: "s", mappingId: "m" }} onMap={first} />,
  );
  view.rerender(
    <DocumentMarkdown
      source="same"
      identity={{ displayedSnapshotId: "s", mappingId: "m" }}
      onMap={(value) => {
        second = value;
      }}
    />,
  );
  const p = view.container.querySelector("p");
  if (!p || !second) throw new Error("no second binding");
  expect((second as BoundDocumentMap).capture(selectionFor(p))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 0, end: 4 },
  });
});
test("keeps authored HTML inert and dangerous Markdown links defanged", () => {
  const { container } = mapped(
    '<img src=x onerror="window.pwned=1">\n<script>window.pwned=2</script>\n\n[bad](javascript:alert(1))',
  );
  expect(container.querySelector("img,script,[onerror]")).toBeNull();
  expect(container.textContent).toContain("<script>");
  expect(container.querySelector("a")?.getAttribute("href")).toBeNull();
});
test("removed image content and code labels have no verified source fragment", () => {
  const { container, map } = mapped("![same](x)\n\n```ts\nsame\n```\n");
  expect(container.querySelector("img")).toBeNull();
  expect(map.rangesFor({ start: 2, end: 6 })).toEqual([]);
  const label = container.querySelector("pre")?.previousElementSibling;
  if (!label) throw new Error("missing code label");
  expect(map.capture(selectionFor(label))).toEqual({ state: "unmapped", reason: "chrome" });
});
test("captures surviving prose across a sanitizer-omitted image", () => {
  const { container, map } = mapped("first ![middle](x) last");
  const paragraph = container.querySelector("p");
  if (!paragraph) throw new Error("missing paragraph");
  expect(container.querySelector("img")).toBeNull();
  expect(map.capture(selectionFor(paragraph))).toEqual({
    state: "mapped",
    sourceRange: { start: 0, end: 23 },
    selectedRenderedText: "first  last",
  });
  expect(
    map
      .rangesFor({ start: 0, end: 23 })
      .map((range) => range.toString())
      .join(""),
  ).toBe("first  last");
  expect(map.rangesFor({ start: 8, end: 14 })).toEqual([]);
});
test("does not trust an unchanged run marker with altered sanitized node text", () => {
  const rendered = renderDocument("same", { displayedSnapshotId: "s", mappingId: "m" });
  const root = document.createElement("div");
  document.body.append(root);
  const segment = rendered.segments[0];
  if (segment?.kind !== "markdown") throw new Error("no prose");
  root.innerHTML = segment.html;
  const marker = root.querySelector("[data-document-run]");
  if (!marker) throw new Error("no marker");
  marker.textContent = "xxxx";
  const map = bindDocumentMap(root, rendered.map);
  expect(map.capture(selectionFor(marker))).toEqual({ state: "unmapped", reason: "chrome" });
  expect(map.rangesFor({ start: 0, end: 4 })).toEqual([]);
  map.dispose();
  root.remove();
});
test("Mermaid error source and restored newline are mapped, its error chrome is not", async () => {
  const { container, map } = mapped("```mermaid\r\nbroken same\r\n```\r\n\r\nafter same\r\n");
  await waitFor(() => expect(container.querySelector("pre code")?.textContent).toBe("broken same\n"));
  const code = container.querySelector("pre code");
  if (!code) throw new Error("no fallback code");
  expect(map.capture(selectionFor(code))).toEqual({
    state: "mapped",
    sourceRange: { start: 12, end: 25 },
    selectedRenderedText: "broken same\n",
  });
  const error = Array.from(container.querySelectorAll("p")).find((p) => p.textContent?.startsWith("Couldn't"));
  if (!error) throw new Error("no error message");
  expect(map.capture(selectionFor(error))).toEqual({ state: "unmapped", reason: "chrome" });
  const p = Array.from(container.querySelectorAll("p")).find((p) => p.textContent?.startsWith("after"));
  if (!p?.firstChild) throw new Error("no following prose");
  expect(map.capture(selectionFor(p.firstChild, 6, 10))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 38, end: 42 },
  });
});
test("generated Mermaid labels are excluded while references resolve across its fence", async () => {
  const { container, map } = mapped("[same][ref]\n\n```mermaid\ngraph TD; A-->B\n```\n\n[ref]: /safe\n\nafter same\n");
  await waitFor(() => expect(container.querySelector("svg text")?.textContent).toBe("generated same"));
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector("a")?.getAttribute("href")).toBe("/safe");
  const label = container.querySelector("svg text");
  if (!label) throw new Error("no generated label");
  expect(map.capture(selectionFor(label))).toEqual({ state: "unmapped", reason: "chrome" });
  const anchor = container.querySelector("a");
  if (!anchor) throw new Error("no reference link");
  expect(map.capture(selectionFor(anchor))).toMatchObject({ state: "mapped", sourceRange: { start: 1, end: 5 } });
  const p = Array.from(container.querySelectorAll("p")).find((p) => p.textContent?.startsWith("after"));
  if (!p?.firstChild) throw new Error("no prose after diagram");
  expect(map.capture(selectionFor(p.firstChild, 6, 10))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 65, end: 69 },
  });
});
test("unterminated Mermaid fences remain ordinary mappable code", () => {
  const { container, map } = mapped("```mermaid\nsame\n");
  const code = container.querySelector("pre code");
  if (!code) throw new Error("no ordinary code");
  expect(container.querySelector("svg")).toBeNull();
  expect(map.capture(selectionFor(code))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 11, end: 15 },
    selectedRenderedText: "same",
  });
});
// Real DOM parity detects loss of Marked merging, table alignment, escaping,
// CSS classes or allowlists during shared rendering/provenance extraction.
test.each([
  "# same ###\n\nA **same** and _same_ ~~same~~ `same`.",
  '[safe](https://example.test "title") &amp; \\*ok\\* ![image](x)',
  "| L | C | R |\n| :--- | :---: | ---: |\n| a\\|b | **same** | &amp; |",
  "> first\n>\n> - same\n>   - tail\n\n- first\n\n  second\n\n- third",
  "a\n    code\n\n> - same\nlazy\n",
  "> first\nlazy\n",
  "> same\nlazy\n",
  "> same\r\nlazy\r\n",
  "<script>same &amp; <img src=x></script>\n\n<code>same &amp;</code>",
  "- [x] done\n\n- [ ] todo\n",
  "````js\n\nsame\n\n````\n",
  "`  a\nb  `",
  "[foo\\]bar]: /url\n\nsee [foo\\]bar]\n",
  "http://example.test www.example.test a@example.test",
])("preserves the current sanitized DOM for %j", (source) => {
  const legacy = render(<Markdown source={source} />);
  const mappedView = mapped(source);
  const content = mappedView.container.firstElementChild?.firstElementChild;
  expect(content?.innerHTML).toBe(legacy.container.firstElementChild?.innerHTML);
});

test("unchanged props rerender retains the active verified binding", () => {
  let map: BoundDocumentMap | null = null;
  const callback = (value: BoundDocumentMap | null) => {
    map = value;
  };
  const view = render(
    <DocumentMarkdown source="same" identity={{ displayedSnapshotId: "s", mappingId: "m" }} onMap={callback} />,
  );
  view.rerender(
    <DocumentMarkdown source="same" identity={{ displayedSnapshotId: "s", mappingId: "m" }} onMap={callback} />,
  );
  const p = view.container.querySelector("p");
  if (!p || !map) throw new Error("no binding");
  expect((map as BoundDocumentMap).capture(selectionFor(p))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 0, end: 4 },
  });
});

test("effect replay retains verified text without changing rendered DOM", () => {
  let map: BoundDocumentMap | null = null;
  const view = render(
    <StrictMode>
      <DocumentMarkdown
        source="same"
        identity={{ displayedSnapshotId: "s", mappingId: "m" }}
        onMap={(value) => {
          map = value;
        }}
      />
    </StrictMode>,
  );
  const p = view.container.querySelector("p");
  if (!p || !map) throw new Error("no binding");
  expect(p.firstChild?.nodeType).toBe(Node.TEXT_NODE);
  expect((map as BoundDocumentMap).capture(selectionFor(p))).toEqual({
    state: "mapped",
    sourceRange: { start: 0, end: 4 },
    selectedRenderedText: "same",
  });
});

test("rebinding invalidates the old lease and rejects unrelated runs with identical text", () => {
  const first = renderDocument("same", { displayedSnapshotId: "s1", mappingId: "m1" });
  const segment = first.segments[0];
  if (segment?.kind !== "markdown") throw new Error("no prose");
  const root = document.createElement("div");
  document.body.append(root);
  root.innerHTML = segment.html;
  const old = bindDocumentMap(root, first.map);
  const replay = bindDocumentMap(root, first.map);
  const p = root.querySelector("p");
  if (!p) throw new Error("no text");
  const s = selectionFor(p);
  expect(old.capture(s)).toEqual({ state: "unmapped", reason: "stale" });
  expect(replay.capture(s)).toMatchObject({ state: "mapped", sourceRange: { start: 0, end: 4 } });
  const replacement = renderDocument("\ufeffsame", { displayedSnapshotId: "s2", mappingId: "m2" });
  const different = bindDocumentMap(root, replacement.map);
  expect(replay.capture(s)).toEqual({ state: "unmapped", reason: "stale" });
  expect(different.capture(s)).toEqual({ state: "unmapped", reason: "chrome" });
  expect(different.rangesFor({ start: 1, end: 5 })).toEqual([]);
  different.dispose();
  replay.dispose();
  old.dispose();
  root.remove();
});
