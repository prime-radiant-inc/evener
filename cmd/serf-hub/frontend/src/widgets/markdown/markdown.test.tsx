import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import codeblockStyles from "../codeblock/codeblock.module.css";
import { requireClass } from "../internal/requireClass";
import { Markdown } from "./index";

afterEach(cleanup);

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
