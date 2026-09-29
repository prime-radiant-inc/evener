import type { Token } from "marked";
import { describe, expect, it, vi } from "vitest";
import { markdownLexer } from "./lexer";
import type { LiveSegmentsCache } from "./segments";
import { messageMayContainMermaid, splitMarkdownSegments } from "./segments";

const MERMAID = "```mermaid\ngraph TD; A-->B\n```\n";

// Every link href reachable in a token array, nested inline tokens included.
function collectLinkHrefs(tokens: Token[]): string[] {
  const hrefs: string[] = [];
  for (const token of tokens) {
    if (token.type === "link") hrefs.push((token as { href?: string }).href ?? "");
    if ("tokens" in token) hrefs.push(...collectLinkHrefs(token.tokens ?? []));
  }
  return hrefs;
}

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

  it("falls back to a whole-source lex when a definition sits across a closed diagram", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const source = `See [the docs][d].\n\n${MERMAID}\n[d]: https://example.com\n`;
    const segments = splitLiveMarkdownSegments(source, { current: null });
    const markdown = segments.find((s) => s.kind === "markdown");
    expect(markdown?.kind).toBe("markdown");
    const hrefs = markdown?.kind === "markdown" ? collectLinkHrefs(markdown.tokens) : [];
    // The use resolves only under a whole-source lex; separate head/tail lexes
    // would leave it literal.
    expect(hrefs).toContain("https://example.com");
  });

  it("does not re-lex the head on a cache hit", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    const head = `intro\n\n${MERMAID}`;
    splitLiveMarkdownSegments(`${head}\nfirst tail\n`, cache);
    const spy = vi.spyOn(markdownLexer, "lexer");
    try {
      // Same head, longer def-free tail: the head's text is unchanged, so no
      // lex of the head text may run (the validation lex is a cache-miss cost).
      splitLiveMarkdownSegments(`${head}\nfirst tail and more\n`, cache);
      expect(spy.mock.calls.filter((call) => call[0] === head)).toHaveLength(0);
    } finally {
      spy.mockRestore();
    }
  });

  it("does not re-lex a rejected list-continuation candidate on a cache hit", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    // The fence line-matches the scan but lexes inside the list, so the
    // candidate is rejected and no head is frozen. A steady stream still must
    // not re-run the validation lex: the rejection verdict is a pure function
    // of the candidate text, so the cache keys on the candidate itself.
    const candidate = "- item\n  ```mermaid\n  graph TD; A-->B\n  ```\n";
    splitLiveMarkdownSegments(`${candidate}- second\n`, cache);
    const spy = vi.spyOn(markdownLexer, "lexer");
    try {
      splitLiveMarkdownSegments(`${candidate}- second\n- third\n`, cache);
      expect(spy.mock.calls.filter((call) => call[0] === candidate)).toHaveLength(0);
    } finally {
      spy.mockRestore();
    }
  });

  // Issue #3208: a settled head with a closed diagram used to re-lex the whole
  // prose tail on every token. Now the settled prefix of the tail is cached and
  // only the bounded window re-lexes. The marker in the settled prefix proves
  // the cache serves it: a miss would lex a string containing "SETTLEDTAIL".
  it("does not re-lex the settled prose tail on a cache hit", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    const head = `intro\n\n${MERMAID}`;
    // A settled prefix long enough that a blank-line boundary sits past the
    // window edge (LIVE_TAIL_WINDOW), then a window of its own.
    const settled = `${"SETTLEDTAIL ".repeat(100)}\n\n`;
    const first = `${head}${settled}${"middle prose ".repeat(70)}`;
    const spy = vi.spyOn(markdownLexer, "lexer");
    try {
      // First render is a miss for both the head and the settled prefix, so the
      // prefix IS lexed here - this keeps the assertion below from being vacuous.
      splitLiveMarkdownSegments(first, cache);
      expect(spy.mock.calls.some((call) => (call[0] ?? "").includes("SETTLEDTAIL"))).toBe(true);

      spy.mockClear();
      // A grown window with the same head and settled prefix: the prefix's text
      // is unchanged, so nothing containing the marker may be lexed again...
      const second = splitLiveMarkdownSegments(`${first} plus more`, cache);
      expect(spy.mock.calls.some((call) => (call[0] ?? "").includes("SETTLEDTAIL"))).toBe(false);
      // ...while the window itself still re-lexes.
      expect(spy.mock.calls.some((call) => (call[0] ?? "").includes("plus more"))).toBe(true);
      // One markdown run carrying both parts, after the diagram: the settled
      // tokens plus the streaming window, rendered in one div.
      expect(second.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
      const secondTail = second[2];
      expect(secondTail?.kind === "markdown" ? secondTail.window : undefined).toContain("plus more");
    } finally {
      spy.mockRestore();
    }
  });

  // The windowed tail holds only when the split is sound. A definition in the
  // window, or an open fence there, must still fall back to the whole-source
  // lex (the same gate the single-root path applies), not split at a blank line.
  it("falls back to the whole-source lex when the tail window is unsound", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const head = `intro\n\n${MERMAID}`;
    const settled = `${"SETTLEDTAIL ".repeat(100)}\n\n`;
    const cache: { current: LiveSegmentsCache | null } = { current: null };
    // An open mermaid fence in the window trips TAIL_BLOCK_MARKER, so the whole
    // source lexes as one document, exactly as before this change.
    const withOpenFence = `${head}${settled}${"middle prose ".repeat(70)}\n\n\`\`\`mermaid\ngraph TD; A-->B`;
    const segments = splitLiveMarkdownSegments(withOpenFence, cache);
    // The trailing open fence demotes to a markdown slice; no windowed segment.
    expect(segments.some((s) => s.kind === "markdown" && s.window !== undefined)).toBe(false);
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
