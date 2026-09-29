import { describe, expect, it } from "vitest";
import type { LiveSegmentsCache } from "./segments";
import { messageMayContainMermaid, splitMarkdownSegments } from "./segments";

const MERMAID = "```mermaid\ngraph TD; A-->B\n```\n";

describe("splitMarkdownSegments", () => {
  it("splits a closed mermaid fence out of prose", () => {
    const segments = splitMarkdownSegments(`hello\n\n${MERMAID}\nworld\n`, null);
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
    expect(segments[1]).toMatchObject({ text: "graph TD; A-->B\n" });
  });

  it("keeps link definitions resolving across a diagram (lex-time registry)", () => {
    const segments = splitMarkdownSegments(`See [the docs][d].\n\n${MERMAID}\n[d]: https://example.com\n`, null);
    const use = segments[0];
    const def = segments[2];
    expect(use?.kind).toBe("markdown");
    expect(def?.kind).toBe("markdown");
    // resolution is proven at integration (Task 5); here the def token must
    // land in the tail slice, not vanish into the cut
    expect(JSON.stringify(segments)).toContain("https://example.com");
  });

  it.each([
    ["tilde fences", "~~~mermaid\ngraph TD; A-->B\n~~~\n"],
    ["indented fence", "   ```mermaid\n   graph TD; A-->B\n   ```\n"],
    ["info string after lang", "```mermaid some title\ngraph TD; A-->B\n```\n"],
    ["uppercase lang", "```Mermaid\ngraph TD; A-->B\n```\n"],
    ["CRLF", "```mermaid\r\ngraph TD; A-->B\r\n```\r\n"],
  ])("splits %s", (_name, fence) => {
    expect(splitMarkdownSegments(fence, null).map((s) => s.kind)).toEqual(["mermaid"]);
  });

  it.each([
    ["dot-prefixed lang", "```.mermaid\nx\n```\n"],
    ["suffixed lang", "```mermaidjs\nx\n```\n"],
    ["fence in blockquote", "> ```mermaid\n> graph TD; A-->B\n> ```\n"],
    ["fence in list item", "- ```mermaid\n  graph TD; A-->B\n  ```\n"],
  ])("does not split %s", (_name, source) => {
    expect(splitMarkdownSegments(source, null).map((s) => s.kind)).toEqual(["markdown"]);
  });

  it("demotes a trailing mermaid fence open in the real source", () => {
    const real = "intro\n\n```mermaid\ngraph TD; A-->";
    const closed = `${real}\n\`\`\``;
    const segments = splitMarkdownSegments(closed, real);
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "markdown"]);
    // the demoted tail still renders as a code block downstream
    expect(JSON.stringify(segments[1])).toContain("graph TD");
  });

  it("demotes a bare CRLF mermaid opener with no content", () => {
    const real = "```mermaid\r\n";
    const closed = `${real}\`\`\``;
    expect(splitMarkdownSegments(closed, real).map((s) => s.kind)).toEqual(["markdown"]);
  });

  it("keeps a terminated mermaid fence when a later non-mermaid fence is open", () => {
    const real = `${MERMAID}\n\`\`\`js\nlet x = 1;`;
    const closed = `${real}\n\`\`\``;
    const segments = splitMarkdownSegments(closed, real);
    expect(segments.map((s) => s.kind)).toEqual(["mermaid", "markdown"]);
  });

  it("does not treat a mismatched fence line as the closer", () => {
    // The opener is backtick-fenced; "~~~" is content, not a closer, so the
    // real source still ends in an open mermaid fence and must demote.
    const real = "```mermaid\ngraph TD; A-->\n~~~";
    const closed = `${real}\n\`\`\``;
    expect(splitMarkdownSegments(closed, real).map((s) => s.kind)).toEqual(["markdown"]);
  });
});

describe("splitLiveMarkdownSegments", () => {
  it("serves the frozen head's segments by identity as the tail streams", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    const head = `intro\n\n${MERMAID}`;
    const first = splitLiveMarkdownSegments(head, cache);
    const second = splitLiveMarkdownSegments(`${head}\nfirst tail token`, cache);
    const third = splitLiveMarkdownSegments(`${head}\nfirst tail token plus more`, cache);

    // A wholesale recompute would hand back fresh segment arrays every render;
    // the frozen head must keep object identity so MarkdownSlice's memo hits.
    expect(second[0]).toBe(first[0]);
    expect(second[1]).toBe(first[1]);
    expect(third[0]).toBe(first[0]);
    expect(third[1]).toBe(first[1]);

    expect(third.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
    expect(JSON.stringify(third[2])).toContain("plus more");
  });

  it("demotes an open mermaid fence in the tail", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    const real = `intro\n\n${MERMAID}\ntail prose\n\n\`\`\`mermaid\ngraph TD; A-->`;
    const segments = splitLiveMarkdownSegments(real, cache);
    expect(segments.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown", "markdown"]);
  });

  it("does not freeze a head at a list-item-continuation mermaid fence", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    // The fence is indented under "- item", so it is a list continuation, not a
    // top-level split: the whole is ONE list, exactly as the settled parse sees
    // it. A frozen head here would lex as one list and the tail as a second.
    const streaming = "- item\n  ```mermaid\n  graph TD; A-->B\n  ```\n- second\n";
    expect(splitLiveMarkdownSegments(streaming, cache).map((s) => s.kind)).toEqual(["markdown"]);
  });
});

describe("messageMayContainMermaid", () => {
  it.each([
    ["plain prose", "hello **world**", false],
    ["mermaid fence", MERMAID, true],
    ["tilde fence", "~~~mermaid\nx\n~~~", true],
    ["indented", "  ```mermaid\nx\n```", true],
    ["non-mermaid fence", "```js\nx\n```", false],
  ])("%s", (_name, source, expected) => {
    expect(messageMayContainMermaid(source)).toBe(expected);
  });
});
