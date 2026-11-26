import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import DOMPurify from "dompurify";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import { Markdown } from "./index";
import markdownStyles from "./markdown.module.css";
import { LIVE_TAIL_WINDOW, LIVE_WINDOWED_MIN_LENGTH } from "./segments";
import { closeOpenMarkdown } from "./streaming";

// Issue #6669: the windowed live path closed the same tail window twice per
// frame - once inside splitLiveWindow (whose closedTail the segment dropped)
// and again in the renderer over the same raw tail. The two orders produce the
// same HTML, so no output assertion can see the duplicate: the count is the
// fact. Record every argument closeOpenMarkdown is handed and assert the window
// text is closed once per windowed frame, not twice. The factory delegates to
// the real module, so every other test in this file keeps the production
// behavior; a plain delegating function rather than a vi.fn keeps the file's
// own vi.restoreAllMocks() from wiping the implementation.
const closedTexts = vi.hoisted(() => [] as string[]);
vi.mock("./streaming", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./streaming")>();
  return {
    ...actual,
    closeOpenMarkdown: (source: string) => {
      closedTexts.push(source);
      return actual.closeOpenMarkdown(source);
    },
  };
});

afterEach(() => {
  vi.restoreAllMocks();
});

beforeEach(() => {
  // Bounded bookkeeping: the delegating wrapper records for the whole file, so
  // each test starts from empty instead of inheriting every earlier frame.
  closedTexts.length = 0;
});

const CODEBLOCK_PRE_CLASS = requireClass(codeblockStyles.pre, "codeblock.module.css", "pre");
const CODEBLOCK_CODE_CLASS = requireClass(codeblockStyles.code, "codeblock.module.css", "code");
const CODEBLOCK_LANGUAGE_CLASS = requireClass(codeblockStyles.language, "codeblock.module.css", "language");
const TABLE_WRAP_CLASS = requireClass(markdownStyles.tableWrap, "markdown.module.css", "tableWrap");
const TABLE_REFLOW_CLASS = requireClass(markdownStyles.tableReflow, "markdown.module.css", "tableReflow");
const CELL_VALUE_CLASS = requireClass(markdownStyles.cellValue, "markdown.module.css", "cellValue");

test("renders a heading", () => {
  render(<Markdown source="# Hello" />);
  expect(screen.getByRole("heading", { level: 1, name: "Hello" })).toBeTruthy();
});

test("renders a paragraph", () => {
  render(<Markdown source="Some prose here." />);
  expect(screen.getByText("Some prose here.")).toBeTruthy();
});

test("renders bold and italic emphasis", () => {
  const { container } = render(<Markdown source="**bold** and _italic_" />);
  expect(container.querySelector("strong")?.textContent).toBe("bold");
  expect(container.querySelector("em")?.textContent).toBe("italic");
});

// --- live: auto-closing the stream tail ---------------------------------------
// The `live` prop marks the source as a possibly-truncated stream still in
// flight: constructs left open at the tail are closed before parsing (see
// streaming.ts), so formatting is visible WHILE streaming instead of literal
// marker source. The settled path never passes it, so a genuinely
// unterminated final source stays honestly literal.

test("live closes an unterminated ** at the stream tail - bold renders while still streaming", () => {
  const { container } = render(<Markdown source="the answer is **bo" live />);
  expect(container.querySelector("strong")?.textContent).toBe("bo");
  expect(container.textContent).not.toContain("**");
});

test("live closes an unterminated fenced code block at the stream tail", () => {
  const { container } = render(<Markdown source={"```js\nconst x = 1;"} live />);
  expect(container.querySelector("pre code")?.textContent).toBe("const x = 1;");
});

test("live closes nested emphasis in reverse-open order - the result parses as nested, not literal", () => {
  const { container } = render(<Markdown source="**a *b" live />);
  const strong = container.querySelector("strong");
  expect(strong).not.toBeNull();
  expect(strong?.querySelector("em")?.textContent).toBe("b");
});

test("live leaves an already-balanced source byte-identical (no invented closers)", () => {
  const balanced = render(<Markdown source="**bold** plain" live />);
  const settled = render(<Markdown source="**bold** plain" />);
  expect(balanced.container.innerHTML).toBe(settled.container.innerHTML);
});

test("without live, an unterminated ** stays literal source (the settled contract)", () => {
  const { container } = render(<Markdown source="the answer is **bo" />);
  expect(container.querySelector("strong")).toBeNull();
  expect(container.textContent).toContain("**bo");
});

test("renders a bullet list", () => {
  const { container } = render(<Markdown source={"- one\n- two"} />);
  const items = Array.from(container.querySelectorAll("li")).map((li) => li.textContent);
  expect(items).toEqual(["one", "two"]);
});

test.each([false, true])("preserves ordered-list starting numbers, live=%s", (live) => {
  const { container } = render(<Markdown source={"7. first\n\n   second"} live={live} />);
  const list = container.querySelector("ol");
  expect(list?.start).toBe(7);
  expect(list?.querySelectorAll(":scope > li")).toHaveLength(1);
  expect(list?.querySelectorAll("li > p")).toHaveLength(2);
});

test("ordered-list attributes in raw HTML stay escaped", () => {
  const source = '<ol start="7" onclick="alert(1)"><li>raw</li></ol>';
  const { container } = render(<Markdown source={source} />);
  expect(container.querySelector("ol, li, [onclick]")).toBeNull();
  expect(container.textContent).toBe(source);
});

test("renders a blockquote", () => {
  const { container } = render(<Markdown source="> a quote" />);
  // marked wraps the quoted line in its own <p>, adding formatting
  // whitespace around it that doesn't affect rendering - trimmed here.
  expect(container.querySelector("blockquote")?.textContent?.trim()).toBe("a quote");
});

test("renders a horizontal rule", () => {
  const { container } = render(<Markdown source={"above\n\n---\n\nbelow"} />);
  expect(container.querySelector("hr")).toBeTruthy();
});

test("renders inline code with the mono inline-code class", () => {
  const { container } = render(<Markdown source="see `npm test`" />);
  const code = container.querySelector("code");
  expect(code?.textContent).toBe("npm test");
});

test("a link opens in a new tab without granting it opener access", () => {
  render(<Markdown source="[docs](https://example.com/docs)" />);
  const link = screen.getByRole("link", { name: "docs" });
  expect(link.getAttribute("href")).toBe("https://example.com/docs");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
});

test("a fenced code block reuses CodeBlock's own pre/code classes", () => {
  const { container } = render(<Markdown source={"```\nconst x = 1;\n```"} />);
  const pre = container.querySelector("pre");
  expect(pre?.classList.contains(CODEBLOCK_PRE_CLASS)).toBe(true);
  expect(pre?.querySelector("code")?.classList.contains(CODEBLOCK_CODE_CLASS)).toBe(true);
  expect(pre?.textContent).toBe("const x = 1;");
});

test("a fenced code block with a language shows CodeBlock's language label", () => {
  const { container } = render(<Markdown source={"```go\nfunc main() {}\n```"} />);
  const label = container.querySelector(`.${CODEBLOCK_LANGUAGE_CLASS}`);
  expect(label?.textContent).toBe("go");
});

test("a fenced code block without a language shows no language label", () => {
  const { container } = render(<Markdown source={"```\nconst x = 1;\n```"} />);
  expect(container.querySelector(`.${CODEBLOCK_LANGUAGE_CLASS}`)).toBeNull();
});

// --- sanitization: the required-by-spec proof that DOMPurify is doing real
// work, plus the two other realistic markdown-specific injection vectors.

test("strips a literal <script> tag embedded in the source, showing it as inert text instead", () => {
  const { container } = render(<Markdown source={"Before <script>window.__markdownXss = true;</script> after"} />);
  expect(container.querySelector("script")).toBeNull();
  expect((window as unknown as { __markdownXss?: boolean }).__markdownXss).toBeUndefined();
  // "no raw HTML passthrough by default": the literal tag text is shown,
  // escaped, rather than silently disappearing - proves it was neutralized
  // by rendering it inert, not by accidentally dropping content.
  expect(container.textContent).toContain("<script>window.__markdownXss = true;</script>");
});

test("neutralizes a javascript: URL scheme on a markdown-syntax link", () => {
  render(<Markdown source="[click me](javascript:alert(1))" />);
  // DOMPurify drops the href attribute entirely for a disallowed URI
  // scheme, rather than passing a defanged value through - which also
  // means the element loses its implicit "link" role (an <a> without an
  // href isn't one per the HTML/ARIA spec), so this queries by text
  // instead of by role.
  const anchor = screen.getByText("click me");
  expect(anchor.tagName).toBe("A");
  expect(anchor.getAttribute("href")).toBeNull();
});

test("strips an event-handler attribute from raw HTML embedded in the source", () => {
  const { container } = render(<Markdown source={'<img src="x" onerror="window.__markdownPwned = true">'} />);
  expect(container.querySelector("img")).toBeNull();
  expect((window as unknown as { __markdownPwned?: boolean }).__markdownPwned).toBeUndefined();
});

test("does not render a real element for a raw HTML tag typed in the source", () => {
  const { container } = render(<Markdown source='<div class="fake-root">nested</div>' />);
  // Only the widget's own root div should exist - the authored <div> must
  // not become a second, real element.
  expect(container.querySelectorAll("div")).toHaveLength(1);
  expect(container.textContent).toContain('<div class="fake-root">nested</div>');
});

test("declares a :focus-visible rule for links, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "markdown.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

test("takes its body ink from --markdown-ink, defaulting to --ink-hi", () => {
  // Comments are stripped before matching: a doc comment in this file quotes
  // the very declaration being asserted, and a sibling contract test once
  // passed against a deleted implementation for exactly that reason.
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  expect(css).toContain("color: var(--markdown-ink, var(--ink-hi))");
});

test("strong emphasis composes through --markdown-strong-weight, defaulting to semibold", () => {
  // Same comment-stripped stylesheet read as the ink seam above: a host
  // that already renders its body at the semibold weight (the ask question
  // cards) opts strong into the bold token through this seam, or bold
  // spans would be indistinguishable from the surrounding text.
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  expect(css).toContain("font-weight: var(--markdown-strong-weight, var(--font-weight-semibold))");
});

test("compact bubbles override the Markdown body ink with the quiet token", () => {
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  const rule = css.match(/\.markdownBubbleCompact\s*\{[^}]*\}/)?.[0] ?? "";
  expect(rule).toContain("--markdown-ink: var(--ink-mid)");
});

// --- agent prose is the transcript's hero (kata 7pa0) -----------------------
// jsdom computes no cascade, so - like the ink assertion above - these read
// the stylesheet's own source rather than a rendered element's computed
// style. streamingtext.module.css exposes the same --prose-font-size hook
// with its own fallback (the body step, not 1em); agent messages no longer
// depend on that pairing for size parity, since their live and settled
// paths are both this same Markdown component.

// The root composes with its host: a host's own font-size applies unless the
// surface pins --prose-font-size (reading surfaces pin it to the prose step).
test("takes its font-size from --prose-font-size, composing with its host at 1em when unset", () => {
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  expect(css).toContain("font-size: var(--prose-font-size, 1em)");
});

test("inline code is a quiet underline, not a filled chip: no background, no radius, sized relative to the surrounding prose", () => {
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  const rule = css.match(/\.inlineCode\s*\{[^}]*\}/)?.[0] ?? "";
  expect(rule).not.toBe("");
  expect(rule).not.toMatch(/background/);
  expect(rule).not.toMatch(/border-radius/);
  expect(rule).not.toMatch(/padding/);
  expect(rule).toContain("border-bottom: 1px solid var(--edge)");
  // Relative (em), not a ramp token: this same rule renders at more than one
  // size across hosts (reading surfaces pin the 18px prose step; dense chrome
  // inherits its host's smaller step), and only a relative size tracks both.
  expect(rule).toMatch(/font-size:\s*0\.86em/);
});

// Overflow containment (2026-07-30-mobile-session-layout-design.md, decision
// 5): prose wraps long tokens - a 90-char path or URL is transcript reality,
// and without an anywhere-break it escapes the bubble, the turn, and
// ultimately the pane (the phone screenshot that motivated the spec).
test("the root rule wraps long unbreakable tokens anywhere", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "markdown.module.css"), "utf8");
  const root = css.match(/\.root \{([^}]*)\}/);
  expect(root).not.toBeNull();
  expect(root![1]).toContain("overflow-wrap: anywhere");
});

// --- GFM tables -------------------------------------------------------------
// marked tokenizes GFM pipe tables (gfm: true) into <table> markup; the
// DOMPurify allowlist must keep every table tag so the structure survives
// sanitization instead of collapsing to unwrapped cell text (headers dropped
// entirely). These guard against re-removing the tags from the allowlist.

test("renders a GFM table with header and body rows", () => {
  const { container } = render(
    <Markdown source={"| Name | Value |\n| ---- | ----- |\n| foo | 123 |\n| bar | 456 |"} />,
  );
  expect(container.querySelector("table")).not.toBeNull();
  expect(container.querySelectorAll("thead tr th")).toHaveLength(2);
  expect(container.querySelectorAll("thead tr th")[0]?.textContent).toBe("Name");
  expect(container.querySelectorAll("tbody tr")).toHaveLength(2);
  expect(container.querySelectorAll("tbody tr")[0]?.querySelectorAll("td")).toHaveLength(2);
  expect(container.querySelectorAll("tbody tr")[0]?.querySelectorAll("td")[0]?.textContent).toBe("foo");
});

test("renders GFM column alignment via the align attribute", () => {
  const { container } = render(<Markdown source={"| L | C | R |\n| :--- | :---: | ---: |\n| a | b | c |"} />);
  const ths = Array.from(container.querySelectorAll("thead th"));
  expect(ths.map((th) => th.getAttribute("align"))).toEqual(["left", "center", "right"]);
  const tds = Array.from(container.querySelectorAll("tbody tr:first-child td"));
  expect(tds.map((td) => td.getAttribute("align"))).toEqual(["left", "center", "right"]);
});

test("wraps GFM tables for container-scoped reflow, marks wide tables with body rows, and labels cells with plain header text", () => {
  const wide = [
    "| `Variant` | **Tokens** | [Delta](https://example.com) |  | Latency & `p95` |",
    "| :--- | ---: | ---: | :--- | ---: |",
    "| `ethos` (v1) | 9,541 | -43.9% | note | 13.4 s |",
  ].join("\n");
  const { container } = render(<Markdown source={wide} />);
  const table = container.querySelector("table");
  const wrap = table?.parentElement;
  expect(wrap?.tagName).toBe("DIV");
  expect(wrap).not.toBe(container.firstElementChild);
  expect(wrap?.classList.contains(TABLE_WRAP_CLASS)).toBe(true);
  expect(wrap?.classList.contains(TABLE_REFLOW_CLASS)).toBe(true);

  const tds = Array.from(container.querySelectorAll("tbody tr:first-child td"));
  expect(tds.map((td) => td.getAttribute("data-label"))).toEqual(["Variant", "Tokens", "Delta", null, "Latency & p95"]);
  for (const td of tds) {
    const value = td.firstElementChild;
    expect(value?.tagName).toBe("SPAN");
    expect(value?.classList.contains(CELL_VALUE_CLASS)).toBe(true);
  }
  // Mixed inline children stay inside the single value wrapper so a grid cell never splits them across tracks.
  expect(tds[0]?.firstElementChild?.querySelector("code")?.textContent).toBe("ethos");
  expect(tds[0]?.firstElementChild?.textContent).toBe("ethos (v1)");
});

test("does not mark narrow (<=4 column) or header-only wide tables for list reflow", () => {
  const narrow = render(<Markdown source={"| A | B | C | D |\n| - | - | - | - |\n| 1 | 2 | 3 | 4 |"} />);
  const narrowWrap = narrow.container.querySelector("table")?.parentElement;
  expect(narrowWrap?.classList.contains(TABLE_WRAP_CLASS)).toBe(true);
  expect(narrowWrap?.classList.contains(TABLE_REFLOW_CLASS)).toBe(false);

  const headerOnly = render(<Markdown source={"| A | B | C | D | E |\n| - | - | - | - | - |"} />);
  const headerOnlyWrap = headerOnly.container.querySelector("table")?.parentElement;
  expect(headerOnlyWrap?.classList.contains(TABLE_WRAP_CLASS)).toBe(true);
  expect(headerOnlyWrap?.classList.contains(TABLE_REFLOW_CLASS)).toBe(false);
});

test("still escapes authored raw HTML carrying data-label attributes", () => {
  const { container } = render(<Markdown source='<div data-label="spoofed">text</div>' />);
  expect(container.querySelector("[data-label]")).toBeNull();
  expect(container.textContent).toContain('<div data-label="spoofed">text</div>');
});

// --- windowed live path: link definitions across the split ------------------
// Past the live-window threshold the live path may serve the head from cache
// and re-parse only the tail - but a link definition on either side of the
// split registers globally with marked, so the halves must never parse
// independently while one is present. These shapes all evade the
// HEAD_SPLIT_HAZARD line pattern (an escaped label like `[foo\]bar]` has no
// `[...]:` span for it to match; a definition indented as a list-item
// continuation sits past its 0-3-space allowance) and are caught by the
// shared-lexer gate instead. Each case asserts the live render matches the
// settled render exactly, and that the settled render really resolves the
// reference - so the test cannot pass vacuously with the link left literal in
// both.
function expectLiveMatchesSettled(source: string, url: string) {
  const live = render(<Markdown source={source} live />);
  const settled = render(<Markdown source={source} />);
  expect(settled.container.querySelector(`a[href="${url}"]`)).not.toBeNull();
  expect(live.container.innerHTML).toBe(settled.container.innerHTML);
}

test("live matches settled when an escaped-label definition sits in the head", () => {
  const source = `${"x".repeat(1200)}\n\n[foo\\]bar]: /url\n\nsee [foo\\]bar] here ${"y".repeat(1050)}`;
  expectLiveMatchesSettled(source, "/url");
});

test("live matches settled when a list-continuation-indented definition sits in the head", () => {
  const source = `${"x".repeat(1100)}\n\n- item\n\n    [lbl]: /url\n\nsee [lbl] here ${"y".repeat(1050)}`;
  expectLiveMatchesSettled(source, "/url");
});

test("live matches settled when a blockquote-nested escaped-label definition sits in the head", () => {
  const source = `${"x".repeat(1100)}\n\n> [q\\]z]: /url\n\nsee [q\\]z] here ${"y".repeat(1050)}`;
  expectLiveMatchesSettled(source, "/url");
});

test("live matches settled when the definition sits in the tail", () => {
  const source = `${"z".repeat(1150)}\n\nsee [t\\]d] here\n\n[t\\]d]: /url\n${"w".repeat(1050)}`;
  expectLiveMatchesSettled(source, "/url");
});

// --- the streaming contract, frame by frame ---------------------------------
// A live render's job is to show the CLOSED version of the source it has: the
// settled render of closeOpenMarkdown(prefix). That holds on every frame,
// including frames that truncate a fence or an emphasis marker, and it holds
// whether the frame took the windowed split or the whole-document fallback.
// This is the contract any incremental parse must preserve exactly: the
// windowed path serves a settled head from cache and parses only the tail, so
// a split that changes the HTML shows up here as a frame that disagrees.
//
// The stream is built to cross the live-window threshold and to carry all four
// constructs a naive split breaks on - a fence, a list, a blockquote and a
// table - and it ENDS inside a fence, so the truncating frames are covered.
function streamingContractDoc(): string {
  const para = "The quick brown fox jumps over the lazy dog while the stream grows. ";
  return [
    para.repeat(6).trim(),
    "",
    "```ts",
    "export function add(a: number, b: number): number {",
    "  return a + b;",
    "}",
    "```",
    "",
    para.repeat(6).trim(),
    "",
    "- first item",
    "- second item",
    "- third item",
    "",
    para.repeat(6).trim(),
    "",
    "> quoted line one",
    "> quoted line two",
    "",
    para.repeat(6).trim(),
    "",
    "| left | center | right |",
    "| :--- | :---: | ---: |",
    "| a | b | c |",
    "| d | e | f |",
    "",
    para.repeat(6).trim(),
    "",
    "```python",
    "def streamed():",
    "    return 'tail'",
    "```",
    "",
  ].join("\n");
}

test("live matches the settled render of the closed source on every frame of a block-carrying stream", () => {
  const source = streamingContractDoc();
  expect(source.length).toBeGreaterThan(2000);
  const { container } = render(<Markdown source={source.slice(0, 50)} live />);
  const mismatched: Array<{ at: number; live: string; settled: string }> = [];
  // Coverage of the four constructs and of a TRUNCATED one, so this test
  // cannot pass by never rendering them: each flag is set by a frame's own
  // live HTML, and the last by a frame whose source needed a synthesized
  // closer.
  const seen = { fence: false, list: false, quote: false, table: false, truncated: false };
  let frames = 0;
  for (let end = 50; end <= source.length; end += 50) {
    const prefix = source.slice(0, end);
    frames += 1;
    // The live render of this frame...
    const live = render(<Markdown source={prefix} live />);
    const liveHtml = live.container.innerHTML;
    live.unmount();
    if (liveHtml.includes("<pre")) seen.fence = true;
    if (liveHtml.includes("<li>")) seen.list = true;
    if (liveHtml.includes("<blockquote>")) seen.quote = true;
    if (liveHtml.includes("<table>")) seen.table = true;
    if (closeOpenMarkdown(prefix) !== prefix) seen.truncated = true;
    // ...must equal a settled render of the same text CLOSED the same way.
    const expected = render(<Markdown source={closeOpenMarkdown(prefix)} />);
    const expectedHtml = expected.container.innerHTML;
    expected.unmount();
    if (liveHtml !== expectedHtml) mismatched.push({ at: end, live: liveHtml, settled: expectedHtml });
  }
  expect(frames).toBeGreaterThan(30);
  expect(seen).toEqual({ fence: true, list: true, quote: true, table: true, truncated: true });
  expect(mismatched).toEqual([]);
  // The mount above is only a container placeholder for the loop's renders.
  expect(container).toBeTruthy();
}, 120_000);

// --- the list-continuation gate, frame by frame ------------------------------
// A loose list (a blank line between items) is one list when parsed whole but
// two when the head and the streaming tail are parsed apart, so it is the only
// split the gate has to refuse. The frames below put the split boundary right
// AFTER a list, past the live-window threshold, which is the shape the
// block-carrying stream above never reaches: there the first blank line at or
// after the window edge always lands past the head's list, so the head never
// ends in one and listFamily is null on every windowed frame.
//
// Each list is long enough that the boundary stays after it for every tail
// length used: splitLiveSource takes the FIRST blank line at/after the window
// edge, which is the blank line after the list only while the tail is shorter
// than LIVE_TAIL_WINDOW.
const SPLIT_LIST_HEAD_BULLET = Array.from(
  { length: 30 },
  (_, i) => `- bullet item ${i + 1} with enough words to fill out a line`,
).join("\n");
const SPLIT_LIST_HEAD_ORDERED = Array.from(
  { length: 30 },
  (_, i) => `${i + 1}. ordered item ${i + 1} with enough words to fill out a line`,
).join("\n");

// A source whose only blank-line boundary at/after the window edge sits
// between `list` and the streaming tail, so the head always ends with the list.
function listAtTheSplit(list: string, tailFirstLine: string, tailLength: number): string {
  const filler = "Filler prose before the list, one paragraph with no blank line inside it. ".repeat(14).trim();
  const pad = "and the streaming tail keeps arriving with more prose.";
  let tail = tailFirstLine;
  while (tail.length < tailLength) tail += ` ${pad}`;
  // The tail's own trailing text never starts a line, so it cannot open a block.
  return `${filler}\n\n${list}\n\n${tail.slice(0, tailLength)}`;
}

// Tail lengths spanning most of the window: every frame here is past the
// threshold, and the split boundary is the blank line after the list because
// the tail stays shorter than the window the boundary is measured from.
const SPLIT_TAIL_LENGTHS = [100, 225, 350, 475, 600, 725, 850, 975].filter((length) => length < LIVE_TAIL_WINDOW);

test.each([
  ["same bullet family (- then -)", SPLIT_LIST_HEAD_BULLET, "- bullet item 31, same family"],
  ["same ordered family (. then .)", SPLIT_LIST_HEAD_ORDERED, "31. ordered item 31, same family"],
])("the list gate declines the split, live matches settled: %s", (_name, list, tailFirstLine) => {
  for (const tailLength of SPLIT_TAIL_LENGTHS) {
    const source = listAtTheSplit(list, tailFirstLine, tailLength);
    expect(source.length).toBeGreaterThan(2000);
    // Declined means the whole source parses once, exactly as it would have
    // before the windowing existed: one sanitize call live, one settled. A
    // split would make it two live, which is what the assertion below catches
    // if the gate is ever dropped.
    const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
    const live = render(<Markdown source={source} live />);
    const liveCalls = sanitizeSpy.mock.calls.length;
    sanitizeSpy.mockClear();
    const settled = render(<Markdown source={source} />);
    const settledCalls = sanitizeSpy.mock.calls.length;
    sanitizeSpy.mockRestore();
    expect({ tailLength, liveCalls, settledCalls }).toEqual({ tailLength, liveCalls: 1, settledCalls: 1 });
    // One list either way - the failure a dropped gate produces is two.
    expect(live.container.innerHTML).toBe(settled.container.innerHTML);
    const liveLists = live.container.querySelectorAll("ul, ol");
    expect(liveLists).toHaveLength(1);
    expect(liveLists[0]?.querySelectorAll("li")).toHaveLength(31);
    live.unmount();
    settled.unmount();
  }
});

test.each([
  // A different family is genuinely two lists, so the split is sound and the
  // expectation is two - while the split is what keeps the head out of the
  // re-parsed tail.
  ["different bullet family (- then +)", SPLIT_LIST_HEAD_BULLET, "+ bullet item 31, different family", 2],
  ["a fence after an ordered list", SPLIT_LIST_HEAD_ORDERED, "```ts\nconst x = 1;\n```", 1],
])("the list gate allows the split, live matches settled: %s", (_name, list, tailFirstLine, expectedLists) => {
  for (const tailLength of SPLIT_TAIL_LENGTHS) {
    const source = listAtTheSplit(list, tailFirstLine, tailLength);
    expect(source.length).toBeGreaterThan(2000);
    // Engagement proof: the windowed path sanitizes head and tail separately
    // (two live calls) where the fallback parses the whole source once, and the
    // settled render is one call either way. Without this the equality below
    // would also pass on the declined path.
    const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
    const live = render(<Markdown source={source} live />);
    const liveCalls = sanitizeSpy.mock.calls.length;
    sanitizeSpy.mockClear();
    const settled = render(<Markdown source={source} />);
    const settledCalls = sanitizeSpy.mock.calls.length;
    sanitizeSpy.mockRestore();
    expect({ tailLength, liveCalls, settledCalls }).toEqual({ tailLength, liveCalls: 2, settledCalls: 1 });
    expect(live.container.innerHTML).toBe(settled.container.innerHTML);
    // The head's list is intact and the tail opened only the block its own
    // first line calls for.
    expect(live.container.querySelectorAll("ul, ol")).toHaveLength(expectedLists);
    expect(live.container.querySelectorAll("ul, ol")[0]?.querySelectorAll("li")).toHaveLength(30);
    live.unmount();
    settled.unmount();
  }
});

// The CRLF twin of the same-family decline: an EMPTY same-family item at the
// split (`-\r\n`) still joins the head's list under marked, which normalizes
// CRLF. The marker patterns allow a line ending in \r for exactly this shape.
// Without that allowance the family reads null, the split is allowed, and the
// empty item renders as a SECOND one-item list until the stream settles.
test.each([
  ["bullet", Array.from({ length: 30 }, (_, i) => `- bullet item ${i + 1} with enough words to fill out a line`), "-"],
  [
    "ordered",
    Array.from({ length: 30 }, (_, i) => `${i + 1}. ordered item ${i + 1} with enough words to fill out a line`),
    "31.",
  ],
])("the list gate declines an empty same-family item at a CRLF split: %s", (_name, headItems, markerLine) => {
  const filler = "Filler prose before the list, one paragraph with no blank line inside it. ".repeat(9).trim();
  let rest = "and the streaming tail keeps arriving with more prose.";
  while (rest.length < 900) rest += " and more streaming prose arrives here.";
  // Built in LF and converted ONCE: the empty item is a marker line whose
  // content is empty, so the tail's first non-blank line is `-` (or `31.`).
  const source = `${filler}\n\n${headItems.join("\n")}\n\n${markerLine}\n${rest}`.replace(/\n/g, "\r\n");
  expect(source).not.toContain("\n\n");
  expect(source.length).toBeGreaterThan(2000);
  const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
  const live = render(<Markdown source={source} live />);
  const liveCalls = sanitizeSpy.mock.calls.length;
  sanitizeSpy.mockRestore();
  const settled = render(<Markdown source={source} />);
  // Declined: one whole-source sanitize, and the empty item stays in the one
  // list. A split would make it two live sanitize calls and two lists.
  expect(liveCalls).toBe(1);
  expect(live.container.innerHTML).toBe(settled.container.innerHTML);
  expect(live.container.querySelectorAll("ul, ol")).toHaveLength(1);
  expect(live.container.querySelectorAll("ul, ol")[0]?.querySelectorAll("li")).toHaveLength(31);
  live.unmount();
  settled.unmount();
});

// --- per-delta work ------------------------------------------------
// The cost this bounds is the one the issue names: a growing document that is
// RE-PARSED ENTIRELY on every delta. Past the live-window threshold the
// windowed split serves the settled head from cache and re-parses only the
// tail, so the characters sanitized per frame stay near the tail window
// instead of tracking the document length. Frames that cross a new
// blank-line boundary re-parse the grown head too, so the assertion counts
// how MANY frames did more than a bounded amount of work rather than
// forbidding any frame from doing it.
const WORK_BOUND_CHARS = 2400;

function framesOverWorkBound(
  source: string,
  delta: number,
): { frames: number; over: number; total: number; pastFrames: number; pastTotal: number } {
  const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
  const { rerender, unmount } = render(<Markdown source={source.slice(0, delta)} live />);
  let frames = 0;
  let over = 0;
  let total = 0;
  let pastFrames = 0;
  let pastTotal = 0;
  for (let end = delta; end <= source.length; end += delta) {
    sanitizeSpy.mockClear();
    rerender(<Markdown source={source.slice(0, end)} live />);
    frames += 1;
    let chars = 0;
    for (const call of sanitizeSpy.mock.calls) chars += String(call[0]).length;
    total += chars;
    // Frames at/under the threshold never window, so they say nothing about
    // the throttle and would dilute an average with whole-document parses.
    if (end > LIVE_WINDOWED_MIN_LENGTH) {
      pastFrames += 1;
      pastTotal += chars;
    }
    if (chars > WORK_BOUND_CHARS) over += 1;
  }
  unmount();
  sanitizeSpy.mockRestore();
  return { frames, over, total, pastFrames, pastTotal };
}

test("a block-carrying live stream re-parses a bounded tail per delta, not the whole document", () => {
  const source = streamingContractDoc();
  const { frames, over, pastFrames, pastTotal } = framesOverWorkBound(source, 40);
  expect(frames).toBeGreaterThan(30);
  // The whole-document fallback re-parses (and re-sanitizes) everything on
  // nearly every frame, so `over` approaches `frames`. The windowed split
  // keeps all but the handful of head-growth frames under the bound.
  expect(over).toBeLessThanOrEqual(Math.ceil(frames / 8));
  // Over the frames that can actually window, the average characters
  // sanitized stays under the work bound. This is the assertion that fails
  // when the path degrades to the whole-document fallback: that fallback
  // re-sanitizes the whole rendered document on each of these frames, whose
  // average (2,723 chars measured for this source) is above the bound, while
  // the windowed path's average is about half of it. An average over ALL
  // frames cannot make that distinction - the sub-threshold frames and the
  // settled HTML are both small enough to pull it under the source length
  // either way.
  expect(pastFrames).toBeGreaterThan(3);
  expect(pastTotal / pastFrames).toBeLessThan(WORK_BOUND_CHARS);
}, 120_000);

// Companion to the link-definition fallback cases above: this source is
// paragraphs-only, balanced, and definition-free past the live-window
// threshold, so the live path takes the windowed branch (settled head served
// from cache, tail window re-parsed) instead of the full-parse fallback each
// of the cases above forces. Rerendering the same mounted component with a
// tail-only append keeps the split head byte-identical, so the second render
// serves the head from cache (a hit) while re-parsing just the grown tail -
// and each render asserts the live output is byte-identical to the settled
// full parse, so the test cannot pass vacuously on a degraded path.
test("live matches settled on a long definition-free source across tail-growth rerenders (windowed path)", () => {
  const sentence = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. ";
  const head = `${sentence.repeat(12).trim()}\n\n${sentence.repeat(12).trim()}\n\n`;
  // The tail carries its own paragraph break inside the window, so the split
  // boundary sits at most LIVE_TAIL_WINDOW from the end (see splitLiveSource:
  // only the first boundary at/after the window edge engages the path).
  const first = `${head + sentence.repeat(10).trim()}\n\n${sentence.repeat(5).trim()}`;
  expect(first.length).toBeGreaterThan(2000);
  // Engagement proof, not just output equality: the windowed path parses head
  // and tail separately while the full-parse fallback parses once, so the
  // sanitize-call count separates them (live==settled alone holds on either
  // path and could not catch a regression to the fallback).
  const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
  const live = render(<Markdown source={first} live />);
  const settledFirst = render(<Markdown source={first} />);
  expect(live.container.innerHTML).toBe(settledFirst.container.innerHTML);
  // Live first render parses head and tail separately (two calls) and the
  // settled render once: three total. The full-parse fallback would give two
  // (one live + one settled), so this count proves the windowed path engaged.
  expect(sanitizeSpy.mock.calls.length).toBe(3);
  sanitizeSpy.mockClear();
  // Tail-only growth: no new blank line crosses the split, so the head is
  // unchanged and the cached head HTML is reused.
  const second = `${first} ${sentence.trim()}`;
  live.rerender(<Markdown source={second} live />);
  const settledSecond = render(<Markdown source={second} />);
  expect(live.container.innerHTML).toBe(settledSecond.container.innerHTML);
  // Head HTML served from cache: the live rerender sanitizes only the grown
  // tail (one call), and the settled render sanitizes once - two total. A
  // fallback would parse the whole live source too (still two), so this count
  // alone proves the cached-head shape only together with the first render's
  // three; either count dropping to the settled-only shape fails loudly.
  expect(sanitizeSpy.mock.calls.length).toBe(2);
});

// CRLF twin of the windowed-path case: a source whose only paragraph breaks
// are "\r\n\r\n" must split the same way - "\n\n" never appears in it, so a
// split search on LF alone would decline to the fallback instead of engaging.
test("live matches settled on a long CRLF source across tail-growth rerenders (windowed path)", () => {
  const sentence = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. ";
  const head = `${sentence.repeat(12).trim()}\r\n\r\n${sentence.repeat(12).trim()}\r\n\r\n`;
  const first = `${head + sentence.repeat(10).trim()}\r\n\r\n${sentence.repeat(5).trim()}`;
  expect(first).not.toContain("\n\n");
  expect(first.length).toBeGreaterThan(2000);
  const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
  const live = render(<Markdown source={first} live />);
  const settledFirst = render(<Markdown source={first} />);
  expect(live.container.innerHTML).toBe(settledFirst.container.innerHTML);
  expect(sanitizeSpy.mock.calls.length).toBe(3);
  sanitizeSpy.mockClear();
  const second = `${first} ${sentence.trim()}`;
  live.rerender(<Markdown source={second} live />);
  const settledSecond = render(<Markdown source={second} />);
  expect(live.container.innerHTML).toBe(settledSecond.container.innerHTML);
  expect(sanitizeSpy.mock.calls.length).toBe(2);
});

// The table chrome lives in the stylesheet, not on a class the component
// writes, so - like the ink/font-size assertions above - this reads the
// stylesheet's own source. Approved editorial-instrument spec (2026-09-09):
// transparent headers, --edge hairlines, and the legacy align attribute
// honored via attribute selectors (so GFM column alignment works without
// allowing the style attribute through DOMPurify).
test("the stylesheet styles GFM tables: flat headers, edge borders, align selectors", () => {
  const css = readModuleCss(import.meta.url, "markdown.module.css");
  expect(css).toContain(".root :where(table)");
  expect(css).toContain("border-collapse: collapse");
  expect(css).toMatch(/thead th[^{]*\{[^}]*background: transparent/);
  expect(css).toMatch(/--edge/);
  expect(css).toContain('th[align="center"]');
  expect(css).toContain('td[align="right"]');
});

// --- mermaid fences render as inline diagrams -------------------------------
// A message whose source carries a closed mermaid fence splits into prose
// slices and diagram containers (segments.ts), all under one root div that
// carries the caller's ref. The diagram container is present synchronously
// (MermaidDiagram's loading state) whether or not mermaid has rendered yet.

test("renders a mermaid fence as a diagram container, prose as markdown", () => {
  const { container } = render(<Markdown source={"before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter"} />);
  expect(container.querySelectorAll("[data-mermaid-diagram]")).toHaveLength(1);
  expect(container.textContent).toContain("before");
  expect(container.textContent).toContain("after");
});

test("keeps an open mermaid fence a code block while live", () => {
  const { container } = render(<Markdown live source={"before\n\n```mermaid\ngraph TD; A-->"} />);
  expect(container.querySelector("[data-mermaid-diagram]")).toBeNull();
  expect(container.textContent).toContain("graph TD; A-->");
});

test("resolves a link definition used on the far side of a diagram", () => {
  const { container } = render(
    <Markdown source={"See [the docs][d].\n\n```mermaid\ngraph TD; A-->B\n```\n\n[d]: https://example.com\n"} />,
  );
  const link = container.querySelector("a[href='https://example.com']");
  expect(link).not.toBeNull();
});

test("keeps an open mermaid fence a code block when settled", () => {
  // Settled renders must not promote a truncated trailing fence (no closer):
  // the live path demotes it, and the settled path must agree instead of
  // flipping to a diagram (or its error fallback) at settle.
  const { container } = render(<Markdown source={"before\n\n```mermaid\ngraph TD; A-->"} />);
  expect(container.querySelector("[data-mermaid-diagram]")).toBeNull();
  expect(container.textContent).toContain("graph TD; A-->");
});

test("resolves a far-side link definition while live (use before def)", () => {
  const { container } = render(
    <Markdown live source={"See [the docs][d].\n\n```mermaid\ngraph TD; A-->B\n```\n\n[d]: https://example.com\n"} />,
  );
  expect(container.querySelector("a[href='https://example.com']")).not.toBeNull();
});

test("resolves a far-side link definition while live (def before use)", () => {
  const { container } = render(
    <Markdown live source={"[d]: https://example.com\n\n```mermaid\ngraph TD; A-->B\n```\n\nSee [the docs][d].\n"} />,
  );
  expect(container.querySelector("a[href='https://example.com']")).not.toBeNull();
});

// Issue #3208: a settled head holding a closed diagram, then a long streaming
// prose tail. The tail's settled prefix is served from a cache and only the
// bounded window re-parses, but the settled+window pair must still render into
// the same single markdown div as a full parse - a naive extra slice would drop
// the paragraph gap (`.root p:last-child { margin-bottom: 0 }`).
//
// Issue #6669: this path closed the window twice per frame - once inside
// splitLiveWindow, whose closedTail the segment used to drop, and again in the
// renderer over the same raw tail. Both orders parse the same closed string, so
// the HTML is identical and no output assertion can see the duplicate. The
// count is the fact, so it is read here too.
test("live matches settled on a closed-diagram head with a long streaming prose tail", () => {
  const sentence = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. ";
  const paragraph = sentence.repeat(25).trim();
  const source = `intro\n\n\`\`\`mermaid\ngraph TD; A-->B\n\`\`\`\n\n${paragraph}\n\n${paragraph}\n\n${sentence.repeat(8).trim()}`;
  expect(source.length).toBeGreaterThan(2000);
  // The split's tail is exactly this final paragraph (the marker-free frame
  // below is the whole source, so the boundary lands at the last blank line at
  // or past the window edge): that string is the frame's window.
  const tailWindow = sentence.repeat(8).trim();
  // Engagement proof, not just output equality: the windowed path parses the
  // head, the settled tail prefix, and the window separately while the
  // full-parse fallback parses once, so the sanitize-call count separates them.
  const sanitizeSpy = vi.spyOn(DOMPurify, "sanitize");
  closedTexts.length = 0;
  const live = render(<Markdown source={source} live />);
  // Read the count before anything else can fail: one is the floor (the split
  // must close the tail it returns, and the renderer must parse that result as
  // given), zero means the window was dropped, two is issue #6669's defect.
  expect(closedTexts.filter((text) => text === tailWindow)).toHaveLength(1);
  const settled = render(<Markdown source={source} />);
  expect(live.container.innerHTML).toBe(settled.container.innerHTML);
  // Live: intro slice + settled prefix + window = three sanitize calls; settled:
  // intro slice + the whole tail = two. Four total would mean the live render
  // fell back to the single full parse.
  expect(sanitizeSpy.mock.calls.length).toBe(5);
});

// A diagram that lands inside the re-parsed window must not flip to a code
// block while live. Only the FROZEN HEAD is checked for non-markdown segments,
// so when the frozen head is empty - here the last closed mermaid fence is
// list-nested, so the head candidate is rejected and the whole source is the
// tail - an earlier top-level diagram sits inside the window. Windowing that
// would send the diagram through the markdown parse, showing a code block
// until it settled. The gates decline it: the segmented live path takes the
// whole-source lex when the window holds a mermaid fence.
//
// Both fences are PIE charts, so nothing carries `-->` (an HTML comment ender
// that would decline the split for an unrelated reason) and the shape cannot
// pass by accident.
test("a closed diagram inside the live re-parse window stays a diagram", () => {
  const lead = "lead prose before the diagram. ".repeat(40).trim();
  const topLevelPie = '```mermaid\npie title Top\n    "a" : 1\n```\n';
  const nestedPie = '- outer item\n  ```mermaid\n  pie title Nested\n      "b" : 2\n  ```\n';
  const firstWindowParagraph = "window paragraph one. ".repeat(30).trim();
  const secondWindowParagraph = "window paragraph two. ".repeat(4).trim();
  const source = `${lead}\n\n${topLevelPie}\n${nestedPie}\n${firstWindowParagraph}\n\n${secondWindowParagraph}`;
  expect(source).not.toContain("-->");
  expect(source.length).toBeGreaterThan(2000);
  const live = render(<Markdown source={source} live />);
  const settled = render(<Markdown source={source} />);
  // The whole-source lex finds exactly the top-level chart; the nested one is
  // list content and stays a code block in the list, settled and live alike.
  expect(settled.container.querySelectorAll("[data-mermaid-diagram]")).toHaveLength(1);
  expect(live.container.querySelectorAll("[data-mermaid-diagram]")).toHaveLength(1);
  expect(live.container.innerHTML).toBe(settled.container.innerHTML);
  live.unmount();
  settled.unmount();
});

// A file:// destination is how an agent names a report it filed beside the
// session. Stripping the href left a bare anchor that looks like a link and
// goes nowhere; the destination survives here so the transcript's anchor pass
// can decide whether the hub serves it.
test("keeps a file: link destination instead of leaving a bare anchor", () => {
  render(
    <Markdown source="[Full report and retained evidence](file:///state/projects/p/sessions/s1/artifacts/r.md)" />,
  );
  const link = screen.getByRole("link", { name: "Full report and retained evidence" });
  expect(link.getAttribute("href")).toBe("file:///state/projects/p/sessions/s1/artifacts/r.md");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
});

// The scheme list is DOMPurify's own default plus `file`, so a scheme the
// library already allowed must keep working: matrix: is the one that was
// dropped once when the list was copied by hand.
test("keeps a matrix: link, which DOMPurify's default already allowed", () => {
  render(<Markdown source="[chat](matrix:u/room?action=chat)" />);
  expect(screen.getByRole("link", { name: "chat" }).getAttribute("href")).toBe("matrix:u/room?action=chat");
});

test.each(["vbscript:msgbox(1)", "data:text/html,<p>x</p>", "blob:https://x/y", "filex:/a"])(
  "still refuses the %s scheme",
  (href) => {
    render(<Markdown source={`[click me](${href})`} />);
    expect(screen.getByText("click me").getAttribute("href")).toBeNull();
  },
);
