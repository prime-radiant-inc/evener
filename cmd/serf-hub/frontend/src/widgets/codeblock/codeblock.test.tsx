import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import buttonStyles from "../button/button.module.css";
import { requireClass } from "../internal/requireClass";
import { CodeBlock } from "./index";

const QUIET_BUTTON_CLASS = requireClass(buttonStyles.quiet, "button.module.css", "quiet");

// A stylesheet-grep assertion must not be satisfiable by a comment - this repo
// has a precedent of exactly that passing while asserting nothing.
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
});

// jsdom has no Clipboard API of its own, but @testing-library/user-event
// installs a working in-memory stub as soon as userEvent.setup() runs (see
// its Clipboard.js: it defines navigator.clipboard as a getter and resets
// the stub's contents in its own afterEach) - so tests spy on THAT stub's
// writeText, calling userEvent.setup() first, rather than installing a
// competing mock of their own (an earlier version of this file did that and
// its mock was silently shadowed by user-event's getter, so the assertions
// were observing the wrong object).

test("renders the code text", () => {
  render(<CodeBlock text="const x = 1;" />);
  expect(screen.getByText("const x = 1;")).toBeTruthy();
});

test("renders plain source lines through the display-only line callback", () => {
  const renderLine = vi.fn((line: string, lineNumber: number) => `${lineNumber}: ${line}`);

  render(<CodeBlock text={"first\nsecond"} renderLine={renderLine} />);

  expect(renderLine).toHaveBeenCalledTimes(2);
  expect(renderLine).toHaveBeenNthCalledWith(1, "first", 0);
  expect(renderLine).toHaveBeenNthCalledWith(2, "second", 1);
  expect(screen.getByText("0: first")).toBeTruthy();
  expect(screen.getByText("1: second")).toBeTruthy();
});

test("ANSI mode renders Vitest SGR output as styled text instead of escape fragments", () => {
  const vitestOutput =
    "\u001b[2m Test Files \u001b[22m \u001b[1m\u001b[32m283 passed\u001b[39m\u001b[22m\n" +
    "\u001b[2m      Tests \u001b[22m \u001b[1m\u001b[32m4904 passed\u001b[39m\u001b[22m";
  const { container } = render(<CodeBlock text={vitestOutput} ansi />);

  expect(container.querySelector("code")?.textContent).toBe(" Test Files  283 passed\n      Tests  4904 passed");
  expect(screen.getByText("283 passed").closest('[data-ansi-fg="green"]')).toBeTruthy();
  expect(container.querySelector("[data-ansi-dim]")?.textContent).toBe(" Test Files ");
});

test("renders in a <pre><code> pairing", () => {
  const { container } = render(<CodeBlock text="const x = 1;" />);
  expect(container.querySelector("pre > code")).toBeTruthy();
});

test("renders no language label when language is omitted", () => {
  render(<CodeBlock text="const x = 1;" />);
  expect(screen.queryByText("typescript")).toBeNull();
});

test("renders the language label when provided", () => {
  render(<CodeBlock text="const x = 1;" language="typescript" />);
  expect(screen.getByText("typescript")).toBeTruthy();
});

test("renders no line-number gutter by default", () => {
  render(<CodeBlock text={"a\nb\nc"} />);
  // "1" only appears as a line number, never in the sample code itself, so
  // its absence from the tree is a reliable proxy for "no gutter rendered".
  expect(screen.queryByText("1")).toBeNull();
});

test("renders a line-number gutter when showLineNumbers is set", () => {
  render(<CodeBlock text={"a\nb\nc"} showLineNumbers />);
  expect(screen.getByText("1")).toBeTruthy();
  expect(screen.getByText("2")).toBeTruthy();
  expect(screen.getByText("3")).toBeTruthy();
});

test("the gutter numbers are hidden from assistive tech (aria-hidden)", () => {
  const { container } = render(<CodeBlock text={"a\nb"} showLineNumbers />);
  const gutters = container.querySelectorAll('[aria-hidden="true"]');
  const gutterTexts = Array.from(gutters).map((el) => el.textContent);
  expect(gutterTexts).toContain("1");
});

test("clicking Copy writes the full text to the clipboard", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  render(<CodeBlock text="const x = 1;" />);
  await user.click(screen.getByRole("button", { name: "Copy" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith("const x = 1;");
});

test("a caller can keep copied text byte-faithful when rendered text is transformed", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  render(<CodeBlock text="\u001b[32mretained" copyText="original retained bytes" ansi />);

  await user.click(screen.getByRole("button", { name: "Copy" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith("original retained bytes");
});

test("the copy button label flips to Copied after a click, then reverts", async () => {
  // @testing-library/user-event's click() hangs indefinitely once fake
  // timers are active in this environment (reproduced with a bare Button
  // and no clipboard involved at all, so it's an environment/library
  // interaction, not something in CodeBlock) - fireEvent.click sidesteps
  // user-event's internal wait/delay machinery entirely, which is all this
  // test needs, since it isn't asserting anything about realistic pointer
  // sequencing.
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
  });
  vi.useFakeTimers();
  render(<CodeBlock text="const x = 1;" />);

  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
  });
  expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();

  await act(async () => {
    // Generously past CodeBlock's own (unexported) revert delay - this
    // only needs to prove the label eventually reverts, not pin the exact
    // duration.
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
});

test("does not throw when the Clipboard API is unavailable", async () => {
  const user = userEvent.setup();
  // Overrides user-event's own stub (installed by setup(), above) to
  // simulate an embed/browser context with no Clipboard API at all.
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  render(<CodeBlock text="const x = 1;" />);
  await user.click(screen.getByRole("button", { name: "Copy" }));
  // still just says Copy - nothing to confirm, since nothing was copied
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
});

test("the copy button is a real, quiet-variant Button (shares its focus-visible + token contract)", () => {
  render(<CodeBlock text="x" />);
  const button = screen.getByRole("button", { name: "Copy" });
  expect(button.tagName).toBe("BUTTON");
  expect(button.classList.contains(QUIET_BUTTON_CLASS)).toBe(true);
});

// A4: the copy affordance is an ICON inset into the block, not a full-width
// labelled row. The accessible name is the only text.
test("the copy control is icon-only - it renders no visible label text", () => {
  render(<CodeBlock text="x" />);
  const button = screen.getByRole("button", { name: "Copy" });
  expect(button.textContent).toBe("");
  expect(button.querySelector("svg")).toBeTruthy();
  // ...and the header holding it is absolutely positioned, so it costs the
  // block no height of its own.
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "codeblock.module.css"), "utf8");
  expect(stripCssComments(css)).toMatch(/\.header\s*\{[^}]*position:\s*absolute/);
});

test("a caller can name what the copy control copies", () => {
  render(<CodeBlock text="x" copyLabel="Copy output" />);
  expect(screen.getByRole("button", { name: "Copy output" })).toBeTruthy();
});

// A4: wrap, don't scroll. Asserted against the stylesheet with comments
// stripped first - a stylesheet-grep test that matches its own comment prose
// asserts nothing (this repo has that precedent).
test("long lines wrap rather than scrolling horizontally", () => {
  const css = stripCssComments(
    readFileSync(join(dirname(fileURLToPath(import.meta.url)), "codeblock.module.css"), "utf8"),
  );
  expect(css).toMatch(/white-space:\s*pre-wrap/);
  expect(css).not.toMatch(/overflow-x:\s*auto/);
});

test("code content is no larger than a transcript row (caption, not body)", () => {
  const css = stripCssComments(
    readFileSync(join(dirname(fileURLToPath(import.meta.url)), "codeblock.module.css"), "utf8"),
  );
  expect(css).toMatch(/\.code\s*\{[^}]*font-size:\s*var\(--font-size-caption\)/);
});

// CodeBlock's only focusable element is its Copy button, which is a real
// Button - Button's own test suite already covers the :focus-visible rule,
// so it isn't re-asserted here (unlike button.test.tsx/panescaffold.test.tsx,
// codeblock.module.css has no focus rule of its own to check).
test("uses the mono font for code content", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "codeblock.module.css"), "utf8");
  expect(css).toContain("var(--font-mono)");
});

// 67zh: a raw tool-output block (pytest tracebacks, shell dumps) with no
// height cap fills an entire 390px viewport, forcing a reader to scroll
// THROUGH it rather than past it. jsdom reports zero for all layout, so a
// real CSS height cap can't be asserted here (verified in a real browser
// instead, see the kata report) - what IS unit-testable, and what actually
// bounds the DOM's height regardless of viewport, is a LINE-COUNT fold: past
// TAIL_VISIBLE_LINES lines, only the tail renders by default, mirroring
// this codebase's own tailFold (helpers.ts) - keep the tail, not the head,
// because the informative part of a long dump (a pytest FAILURES section)
// is almost always at the end, not the start.
const LONG_LINES = Array.from({ length: 20 }, (_, i) => `line ${i + 1}`);
const LONG_TEXT = LONG_LINES.join("\n");

test("short content renders unfolded, with no fold control", () => {
  render(<CodeBlock text={"a\nb\nc"} />);
  expect(screen.queryByRole("button", { name: /earlier line/ })).toBeNull();
});

// "line 1" alone would substring-match "line 10".."line 19" too - a word
// boundary after the digit is what actually pins down "line 1" the exact
// first line, not any decade of it.
const LINE_1_ONLY = /\bline 1\b(?!\d)/;

test("long content folds to the tail by default, hiding the head behind a count", () => {
  render(<CodeBlock text={LONG_TEXT} />);
  // The tail is visible...
  expect(screen.getByText("line 20", { exact: false })).toBeTruthy();
  // ...but the head is not rendered at all (folded, not just visually capped).
  expect(screen.queryByText(LINE_1_ONLY)).toBeNull();
  expect(screen.getByRole("button", { name: "Show 6 earlier lines" })).toBeTruthy();
});

test("Show N earlier lines reveals the full content and offers Show fewer lines", async () => {
  const user = userEvent.setup();
  render(<CodeBlock text={LONG_TEXT} />);
  await user.click(screen.getByRole("button", { name: "Show 6 earlier lines" }));
  expect(screen.getByText(LINE_1_ONLY)).toBeTruthy();
  expect(screen.getByText("line 20", { exact: false })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Show fewer lines" })).toBeTruthy();
});

test("Show fewer lines re-folds back to the tail", async () => {
  const user = userEvent.setup();
  render(<CodeBlock text={LONG_TEXT} />);
  await user.click(screen.getByRole("button", { name: "Show 6 earlier lines" }));
  await user.click(screen.getByRole("button", { name: "Show fewer lines" }));
  expect(screen.queryByText(LINE_1_ONLY)).toBeNull();
  expect(screen.getByRole("button", { name: "Show 6 earlier lines" })).toBeTruthy();
});

test("a single hidden line is worded in the singular", () => {
  const lines = Array.from({ length: 15 }, (_, i) => `line ${i + 1}`);
  render(<CodeBlock text={lines.join("\n")} />);
  expect(screen.getByRole("button", { name: "Show 1 earlier line" })).toBeTruthy();
});

test("fold={false} renders every line with no fold control at all", () => {
  render(<CodeBlock text={LONG_TEXT} fold={false} />);
  expect(screen.getByText(LINE_1_ONLY)).toBeTruthy();
  expect(screen.getByText("line 20", { exact: false })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /earlier lines/ })).toBeNull();
  expect(screen.queryByRole("button", { name: "Show fewer lines" })).toBeNull();
});

test("the gutter shows real line numbers for the folded tail, not renumbered from 1", () => {
  render(<CodeBlock text={LONG_TEXT} showLineNumbers />);
  // Tail starts at line 15 of 20 (20 - 14 + 1) - its gutter must say "15", not "1".
  expect(screen.getByText("15")).toBeTruthy();
  expect(screen.queryByText("1")).toBeNull();
});

test("Copy always copies the full, unfolded text - even while visually folded", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  render(<CodeBlock text={LONG_TEXT} />);
  await user.click(screen.getByRole("button", { name: "Copy" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(LONG_TEXT);
});

test("ANSI style inherited from a folded earlier line remains on the visible tail", () => {
  const lines = Array.from({ length: 15 }, (_, index) => `${index === 0 ? "\u001b[32m" : ""}line ${index + 1}`);
  const { container } = render(<CodeBlock text={`${lines.join("\n")}\u001b[39m`} ansi />);

  expect(screen.queryByText(LINE_1_ONLY)).toBeNull();
  expect(screen.getByText("line 15").closest('[data-ansi-fg="green"]')).toBeTruthy();
  expect(container.querySelector("code")?.textContent).not.toContain("\u001b");
});

test("Copy preserves the original ANSI-bearing text exactly", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const source = "\u001b[32mgreen\u001b[0m";
  render(<CodeBlock text={source} ansi />);

  await user.click(screen.getByRole("button", { name: "Copy" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(source);
});
