// @vitest-environment node

// The shared secondary-text, verbatim and truncation recipes are pinned HERE,
// once, so each consumer stylesheet can assert that its class composes the
// recipe instead of repeating the declarations the recipe owns. Same off-disk
// contract style as rhythm.test.ts.
import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "./cssBlock";

const css = readModuleCss(import.meta.url, "text.module.css");

test("textSecondary carries the shared secondary-text face, size and ink", () => {
  const secondary = topRuleBlock(css, ".textSecondary");
  expect(secondary).toMatch(/font-family: var\(--font-sans\)/);
  expect(secondary).toMatch(/font-size: var\(--font-size-ui\)/);
  expect(secondary).toMatch(/color: var\(--ink-mid\)/);
});

test("verbatim carries the machine-text recipe; verbatimWrap adds word breaking", () => {
  const verbatim = topRuleBlock(css, ".verbatim");
  expect(verbatim).toMatch(/font-family: var\(--font-mono\)/);
  expect(verbatim).toMatch(/font-size: var\(--font-size-caption\)/);
  expect(verbatim).toMatch(/color: var\(--ink-mid\)/);
  expect(verbatim).toMatch(/white-space: pre-wrap/);
  expect(topRuleBlock(css, ".verbatimWrap")).toMatch(/word-break: break-word/);
});

test("truncate carries the single-line truncation recipe", () => {
  const truncate = topRuleBlock(css, ".truncate");
  expect(truncate).toMatch(/overflow: hidden/);
  expect(truncate).toMatch(/text-overflow: ellipsis/);
  expect(truncate).toMatch(/white-space: nowrap/);
});

test("paneTitle carries the shared pane-title face, size, weight, tracking and leading", () => {
  const paneTitle = topRuleBlock(css, ".paneTitle");
  expect(paneTitle).toMatch(/color: var\(--ink-hi\)/);
  expect(paneTitle).toMatch(/font-family: var\(--font-prose\)/);
  expect(paneTitle).toMatch(/font-size: var\(--font-size-pane-title\)/);
  expect(paneTitle).toMatch(/font-weight: var\(--font-weight-semibold\)/);
  expect(paneTitle).toMatch(/letter-spacing: var\(--tracking-display\)/);
  expect(paneTitle).toMatch(/line-height: var\(--line-height-title\)/);
});

test.each([
  ["../widgets/panescaffold/panescaffold.module.css", ".title"],
  ["../shell/mobile/StackHost.module.css", ".stackTitle"],
])("%s %s composes the shared pane title", (file, selector) => {
  expect(topRuleBlock(readModuleCss(import.meta.url, file), selector)).toMatch(
    /composes:[^;]*\bpaneTitle\b[^;]*from "[./]*styles\/text\.module\.css"/,
  );
});

test("the dialog title keeps its sans face in its own sheet", () => {
  // paneTitle's serif face is emitted from this sheet's chunk, which can load
  // after the dialog's own sheet: a composes reference would tie on
  // specificity and lose to whichever copy lands later. The dialog title
  // therefore declares the full type itself.
  const title = topRuleBlock(readModuleCss(import.meta.url, "../widgets/dialog/dialog.module.css"), ".title");
  expect(title).not.toMatch(/composes:[^;]*\bpaneTitle\b/);
  expect(title).toMatch(/font-family:\s*var\(--font-sans\)/);
  expect(title).toMatch(/font-size:\s*var\(--font-size-pane-title\)/);
  expect(title).toMatch(/font-weight:\s*var\(--font-weight-semibold\)/);
  expect(title).toMatch(/letter-spacing:\s*var\(--tracking-display\)/);
  expect(title).toMatch(/line-height:\s*var\(--line-height-title\)/);
});
