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
  vi.useRealTimers();
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
