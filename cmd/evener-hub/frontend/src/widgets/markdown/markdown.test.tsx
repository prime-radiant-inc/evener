import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import DOMPurify from "dompurify";
import { afterEach, expect, test, vi } from "vitest";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import { Markdown } from "./index";

afterEach(cleanup);
afterEach(() => {
  vi.restoreAllMocks();
});

const CODEBLOCK_PRE_CLASS = requireClass(codeblockStyles.pre, "codeblock.module.css", "pre");
const CODEBLOCK_CODE_CLASS = requireClass(codeblockStyles.code, "codeblock.module.css", "code");
const CODEBLOCK_LANGUAGE_CLASS = requireClass(codeblockStyles.language, "codeblock.module.css", "language");

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
  const here = dirname(fileURLToPath(import.meta.url));
  // Comments are stripped before matching: a doc comment in this file quotes
  // the very declaration being asserted, and a sibling contract test once
  // passed against a deleted implementation for exactly that reason.
  const css = readFileSync(join(here, "markdown.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toContain("color: var(--markdown-ink, var(--ink-hi))");
});

// --- agent prose is the transcript's hero (kata 7pa0) -----------------------
// jsdom computes no cascade, so - like the ink assertion above - these read
// the stylesheet's own source rather than a rendered element's computed
// style. AgentMessageItem.test.tsx asserts the other half of this contract:
// that streamingtext.module.css exposes the identical hook with the
// identical fallback, so the live and settled paths can never disagree.

test("takes its font-size from --prose-font-size, defaulting to --font-size-body", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "markdown.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toContain("font-size: var(--prose-font-size, var(--font-size-body))");
});

test("inline code is a quiet underline, not a filled chip: no background, no radius, sized relative to the surrounding prose", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "markdown.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = css.match(/\.inlineCode\s*\{[^}]*\}/)?.[0] ?? "";
  expect(rule).not.toBe("");
  expect(rule).not.toMatch(/background/);
  expect(rule).not.toMatch(/border-radius/);
  expect(rule).not.toMatch(/padding/);
  expect(rule).toContain("border-bottom: 1px solid var(--edge)");
  // Relative (em), not a ramp token: this same rule renders inside prose at
  // more than one size (agent messages step up to --font-size-pane-title;
  // every other Markdown caller stays at --font-size-body), and only a
  // relative size tracks both.
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

// --- windowed live path: link definitions across the split ------------------
// Past the live-window threshold the live path may serve the head from cache
// and re-parse only the tail - but a link definition on either side of the
// split registers globally with marked, so the halves must never parse
// independently while one is present. These shapes all evade the
// HEAD_SPLIT_HAZARD / TAIL_BLOCK_MARKER line patterns (an escaped label like
// `[foo\]bar]` has no `[...]:` span for them to match; a definition indented
// as a list-item continuation sits past their 0-3-space allowance) and are
// caught by the shared-lexer gate instead. Each case asserts the live render
// matches the settled render exactly, and that the settled render really
// resolves the reference - so the test cannot pass vacuously with the link
// left literal in both.
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
// stylesheet's own source. Guards the table styling contract: a header band
// on --surface-inset, --edge hairlines, and the legacy align attribute
// honored via attribute selectors (so GFM column alignment works without
// allowing the style attribute through DOMPurify).
test("the stylesheet styles GFM tables: header band, edge borders, align selectors", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "markdown.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toContain(".root :where(table)");
  expect(css).toContain("border-collapse: collapse");
  expect(css).toMatch(/thead th[^{]*\{[^}]*--surface-inset/);
  expect(css).toMatch(/--edge/);
  expect(css).toContain('th[align="center"]');
  expect(css).toContain('td[align="right"]');
});
