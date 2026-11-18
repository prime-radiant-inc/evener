import type { Token } from "marked";
import { describe, expect, it, vi } from "vitest";
import { markdownLexer } from "./lexer";
import type { HeadGateVerdict, LiveSegmentsCache } from "./segments";
import { LIST_FAMILY_UNKNOWN, messageMayContainMermaid, splitLiveWindow, splitMarkdownSegments } from "./segments";

const MERMAID = "```mermaid\ngraph TD; A-->B\n```\n";

// A head that ENDS in a list, long enough that the window edge falls inside the
// list, so splitLiveSource's first blank line at/after the edge is the one
// after it. That is the only geometry in which the head's list family is read.
function listSplitHead(): string {
  return Array.from({ length: 30 }, (_, i) => `- bullet item ${i + 1} with enough words to fill out a line`).join("\n");
}

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

// How many times `run` split `tail` on "\n". A duplicate line scan over the
// same tail is invisible in the rendered output and a timing assertion would
// measure the host instead, so the count is the fact: splitLiveWindow's two
// tail gates must share one scan of the tail's first non-blank line.
function countTailLineScans(tail: string, run: () => void): number {
  // A call-through spy, so nothing here restates split's own behavior. Marked
  // splits its input on "\n" too, and the definition check lexes the tail, so
  // the receiver matters as much as the separator: only passes over the exact
  // tail string count.
  const spy = vi.spyOn(String.prototype, "split");
  let scans = 0;
  try {
    run();
    const calls: unknown[][] = spy.mock.calls;
    const contexts: unknown[] = spy.mock.contexts;
    for (const [index, call] of calls.entries()) {
      const separator = call[0];
      if (separator === "\n" && String(contexts[index]) === tail) scans += 1;
    }
  } finally {
    spy.mockRestore();
  }
  return scans;
}

describe("splitLiveWindow tail gates", () => {
  // The tail's first non-blank line decides both gates. The two readings share
  // one scan, so a list-headed tail costs exactly what a prose-headed one does:
  // closeOpenMarkdown's pass (its result is returned as closedTail) plus that
  // one shared read.
  it("reads the tail's first non-blank line once whether or not the head ends in a list", () => {
    const tail = "streaming tail prose. ".repeat(40).trim();
    const listHeaded = splitLiveWindow(`${listSplitHead()}\n\n${tail}`, { current: null });
    const proseHeaded = splitLiveWindow(`${"prose paragraph. ".repeat(40).trim()}\n\n${tail}`, { current: null });
    // Control: both frames reach the tail gates, so the counts below are not
    // measuring an early decline.
    expect(listHeaded?.tail).toBe(tail);
    expect(proseHeaded?.tail).toBe(tail);
    const listScans = countTailLineScans(tail, () => {
      expect(splitLiveWindow(`${listSplitHead()}\n\n${tail}`, { current: null })).not.toBeNull();
    });
    const proseScans = countTailLineScans(tail, () => {
      expect(splitLiveWindow(`${"prose paragraph. ".repeat(40).trim()}\n\n${tail}`, { current: null })).not.toBeNull();
    });
    expect({ listHeaded: listScans, proseHeaded: proseScans }).toEqual({ listHeaded: 2, proseHeaded: 2 });
  });

  // The edge the two readings could disagree on: a tail of nothing but blanks
  // has no first non-blank line, so it reads as NOT indented and does not
  // extend a list. Reading the raw tail's first character instead would call
  // the leading spaces an indent and decline a split that is sound.
  it.each([
    ["an all-blank tail", "   ", true],
    ["an indented first line", "  indented continuation of an open item", false],
    ["non-indented prose", "streaming tail prose. ".repeat(40).trim(), true],
  ])("reads %s correctly", (_name, tail, accepted) => {
    const source = `${listSplitHead()}\n\n${tail}`;
    expect(splitLiveWindow(source, { current: null })?.tail ?? null).toBe(accepted ? tail : null);
  });

  // A declined frame is a steady state - a list-headed stream declines every
  // frame until its list ends - so it must not pay for the whole-tail close
  // whose result it throws away. Both the shared line read and the close split
  // the tail on "\n", so the count is every line pass over it: one for a
  // declined frame, two for an accepted one (the read, then the close).
  it("does not close the tail it is going to discard", () => {
    const inner = "streaming tail prose. ".repeat(40).trim();
    // An indented first line declines the split before the close.
    const declinedTail = `  ${inner}`;
    const declinedScans = countTailLineScans(declinedTail, () => {
      expect(splitLiveWindow(`${listSplitHead()}\n\n${declinedTail}`, { current: null })).toBeNull();
    });
    // The same tail accepted still closes exactly once, which keeps the
    // assertion above from passing vacuously.
    const acceptedScans = countTailLineScans(inner, () => {
      expect(splitLiveWindow(`${listSplitHead()}\n\n${inner}`, { current: null })?.closedTail).toBe(inner);
    });
    expect({ declinedScans, acceptedScans }).toEqual({ declinedScans: 1, acceptedScans: 2 });
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
      expect(secondTail?.kind === "markdown" ? secondTail.closedWindow : undefined).toContain("plus more");
    } finally {
      spy.mockRestore();
    }
  });

  // The windowed tail holds only when the split is sound. A mermaid fence in
  // the window must fall back to the whole-source lex: only the frozen head is
  // checked for non-markdown segments, so a diagram sitting in the window
  // would parse as markdown and flip to a code block until it settled. Both
  // fences here are PIE charts - no `-->` anywhere - so HTML_BLOCK_END cannot
  // be what declines the split, and the whole-source lex is the pinned
  // contract.
  it("falls back to the whole-source lex when the tail window holds a mermaid fence", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const topLevelPie = '```mermaid\npie title Top\n    "a" : 1\n```\n';
    // Indented under a list item, so the scan's LAST closed fence candidate is
    // rejected and no head freezes: the whole source is the tail, and the
    // top-level pie above lands inside the window.
    const nestedPie = '- outer item\n  ```mermaid\n  pie title Nested\n      "b" : 2\n  ```\n';
    const lead = "lead prose before the diagram. ".repeat(40).trim();
    const firstWindowParagraph = "window paragraph one. ".repeat(30).trim();
    const secondWindowParagraph = "window paragraph two. ".repeat(4).trim();
    const source = `${lead}\n\n${topLevelPie}\n${nestedPie}\n${firstWindowParagraph}\n\n${secondWindowParagraph}`;
    expect(source).not.toContain("-->");
    // The split is otherwise sound (a blank-line boundary well inside the
    // window), so only the mermaid check can decline the windowed path.
    const split = splitLiveWindow(source, { current: null });
    expect(split).not.toBeNull();
    expect(messageMayContainMermaid(split?.tail ?? "")).toBe(true);
    const segments = splitLiveMarkdownSegments(source, { current: null });
    // Whole-source lex: the top-level pie is still a diagram, and no windowed
    // markdown segment exists to parse it as a code block instead.
    expect(segments.filter((s) => s.kind === "mermaid")).toHaveLength(1);
    expect(segments.some((s) => s.kind === "markdown" && s.closedWindow !== undefined)).toBe(false);
  });

  // Finding: an unreadable head made the gate FAIL OPEN. The sentinel equals
  // no real marker family, so a bare comparison would allow the split; the
  // gate must decline instead, as headGateVerdict's own contract says. Seeding
  // the cache with an UNKNOWN verdict is the only way to reach the arm today
  // (both real producers - a lexer throw and an unparsable first item - are
  // already declined by hasDef or cannot be produced by marked).
  it("declines the split when the head's list family is unreadable", () => {
    const head = `${listSplitHead()}\n\n`;
    const tail = "streaming tail prose. ".repeat(40).trim();
    const source = head + tail;
    const unreadable: HeadGateVerdict = {
      headSource: head,
      balanced: true,
      hazard: false,
      htmlStart: false,
      hasDef: false,
      listFamily: LIST_FAMILY_UNKNOWN,
    };
    // A real same-family tail would already decline, so the tail here is a
    // paragraph: the ONLY reason to decline is the unreadable head.
    expect(tail.startsWith("streaming")).toBe(true);
    // Control: the same source with a readable paragraph-ending head splits.
    expect(splitLiveWindow(source, { current: null })).not.toBeNull();
    expect(splitLiveWindow(source, { current: unreadable })).toBeNull();
  });

  // Finding: headGateVerdict lexed the head twice per head change (once for
  // the definition check, once for the list family) on exactly the O(n)
  // frames this change counts against its own work bound.
  it("lexes the head once per head change", () => {
    const head = `${listSplitHead()}\n\n`;
    const tail = "streaming tail prose. ".repeat(40).trim();
    const source = head + tail;
    const spy = vi.spyOn(markdownLexer, "lexer");
    try {
      const cache: { current: HeadGateVerdict | null } = { current: null };
      expect(splitLiveWindow(source, cache)).not.toBeNull();
      // The split's head is the string the verdict keyed on.
      expect(splitLiveWindow(source, cache)?.head).toBe(head);
      expect(spy.mock.calls.filter((call) => call[0] === head)).toHaveLength(1);
    } finally {
      spy.mockRestore();
    }
  });

  // A rejected frozen head (a list-item-nested fence) leaves headSource empty,
  // so the tail is the WHOLE source and can still hold an earlier top-level
  // closed diagram. Windowing that prefix must not drop the diagram.
  it("keeps a top-level closed diagram when the frozen head was rejected", async () => {
    const { splitLiveMarkdownSegments } = await import("./segments");
    const source =
      `intro\n\n${MERMAID}\n` +
      "- item\n  ```mermaid\n  graph TD; C-->D\n  ```\n\n" +
      `${"first prose paragraph. ".repeat(70)}\n\n${"second prose paragraph. ".repeat(20)}`;
    expect(source.length).toBeGreaterThan(2000);
    const segments = splitLiveMarkdownSegments(source, { current: null });
    // Exactly the top-level fence is a diagram; the nested one stays in the list.
    expect(segments.filter((s) => s.kind === "mermaid")).toHaveLength(1);
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
