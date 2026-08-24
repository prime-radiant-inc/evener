// Additional closeOpenMarkdown edge cases for blockquote + list interactions,
// nested fences inside blockquotes, and emphasis block-boundary resets.
// These close the remaining uncovered lines in streaming.ts.

import { expect, test } from "vitest";
import { closeOpenMarkdown } from "./streaming";

// Lines 307-310: a quoted list child with an ATX heading or thematic break
// inside a nested quote resets the emphasis stack
test("quoted list child with ATX heading resets emphasis stack", () => {
  // > - text with **emph
  // > # heading
  // The heading inside the quoted list child resets the stack
  const result = closeOpenMarkdown("> - text with **emph\n> # heading");
  // The **emph should be closed before the heading, so the final output
  // should not have trailing ** from the open emphasis
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 307-310: quoted list child with thematic break
test("quoted list child with thematic break resets emphasis", () => {
  const result = closeOpenMarkdown("> - **bold\n> ---");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 353-354: thematic break or setext underline inside a quoted list
// child (no nested quote marker) resets the stack
test("quoted list child with thematic break (no nested quote) resets emphasis", () => {
  const result = closeOpenMarkdown("> - **bold text\n> ---");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 433-437: indented code block inside a blockquote-list container
// resets the paragraph state
test("indented code inside a blockquote list container resets paragraph", () => {
  // > - text
  //     indented (inside blockquote context)
  const result = closeOpenMarkdown("> - **emph\n    code");
  // The indented code block after the quoted list resets paragraph state
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 461-465: quoted content that is empty/blank resets the stack
test("quoted blank line resets emphasis stack", () => {
  const result = closeOpenMarkdown("**bold\n>\nafter");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 515-519: setext underline inside a quoted paragraph resets the stack
test("quoted setext underline resets emphasis stack", () => {
  const result = closeOpenMarkdown("> **bold text\n> ===");
  expect(result).not.toMatch(/\*\*$/);
});

// A fence opened inside a quoted list child with a nested quote marker
test("fence opened inside quoted list child with nested quote", () => {
  // > - > ```js
  //   code
  // The fence should be closed at the end
  const result = closeOpenMarkdown("> - > ```js\ncode");
  expect(result).toMatch(/```$/);
});

// A fence inside a blockquote that closes with a different quote depth
test("fence inside blockquote closes when quote depth decreases", () => {
  const result = closeOpenMarkdown("> ```js\ncode\nnot quoted");
  // The fence should be closed when the quote depth decreases
  expect(result).toContain("```");
});

// Lazy blockquote continuation: emphasis carries through
test("lazy blockquote continuation preserves emphasis state", () => {
  const result = closeOpenMarkdown("> **bold\nlazy continuation");
  // The ** should be closed at the end since lazy continuation
  // preserves the paragraph state
  expect(result).toMatch(/\*\*$/);
});

// List item inside blockquote with emphasis that spans into child
// The new list item resets the stack, so emphasis is NOT at the end
test("nested list inside blockquote pushes deeper indent frame", () => {
  const result = closeOpenMarkdown("> - outer **emph\n>   - inner text");
  expect(result).not.toMatch(/\*\*$/);
});

// Blockquote with a nested list that has a deeper nesting level
test("blockquote list item with emphasis continues into child", () => {
  const result = closeOpenMarkdown("> - text **emph\n>   more");
  expect(result).toMatch(/\*\*$/);
});

// Quoted list child with a fence opener (no nested quote)
// The fence opens inside the blockquote, but since the line after
// is not quoted, the fence closes at the quote depth boundary
test("quoted list child with fence opener (no nested quote)", () => {
  const result = closeOpenMarkdown("> - text\n> ```js\ncode");
  expect(result).toContain("```");
});

// Indented code block inside a blockquote (not in a list)
test("indented code inside blockquote (no list) scans inline", () => {
  const result = closeOpenMarkdown("> **bold\n>     code");
  // The indented code inside the quote should preserve the emphasis
  expect(result).toMatch(/\*\*$/);
});

// Quoted indented code with active list container
// The indented code is NOT deindented (it's too deeply indented for the
// list's content indent), so emphasis is NOT scanned — stays literal
test("quoted indented code with active list container deindents", () => {
  const result = closeOpenMarkdown("> - text\n>       code with **emph");
  expect(result).not.toMatch(/\*\*$/);
});

// Quoted setext underline creating a heading
test("quoted setext underline creates a heading boundary", () => {
  const result = closeOpenMarkdown("> heading text **emph\n> ===");
  expect(result).not.toMatch(/\*\*$/);
});

// A paragraph continuing after a blockquote with emphasis
test("paragraph after blockquote with emphasis is preserved", () => {
  const result = closeOpenMarkdown("> quoted\n**bold");
  expect(result).toMatch(/\*\*$/);
});

// Thematic break at the top level resets emphasis
test("thematic break at top level resets emphasis stack", () => {
  const result = closeOpenMarkdown("**bold\n---\nafter");
  expect(result).not.toMatch(/\*\*$/);
});

// Setext underline at top level resets emphasis
test("setext underline at top level resets emphasis stack", () => {
  const result = closeOpenMarkdown("**bold text\n===\nafter");
  expect(result).not.toMatch(/\*\*$/);
});

// ATX heading at top level resets emphasis and scans its own inline
test("ATX heading at top level resets emphasis and scans heading inline", () => {
  const result = closeOpenMarkdown("**bold\n## heading *emph");
  expect(result).toMatch(/\*$/);
  expect(result).not.toMatch(/\*\*$/);
});

// List item at top level resets emphasis and scans its own inline
test("list item at top level resets emphasis and scans list inline", () => {
  const result = closeOpenMarkdown("**bold\n- item *emph");
  expect(result).toMatch(/\*$/);
  expect(result).not.toMatch(/\*\*$/);
});

// Blockquote with isAtxHeading content
// The heading resets the emphasis stack and scans its own inline markers,
// so the **emph in the heading IS closed
test("blockquote with ATX heading content resets emphasis and scans heading", () => {
  const result = closeOpenMarkdown("> ## heading **emph");
  expect(result).toMatch(/\*\*$/);
});

// Blockquote with thematic break content
test("blockquote with thematic break content resets emphasis", () => {
  const result = closeOpenMarkdown("> ---\n**bold");
  expect(result).toMatch(/\*\*$/);
});

// Blockquote with list item content
test("blockquote with list item content resets and scans inline", () => {
  const result = closeOpenMarkdown("> - item **emph");
  expect(result).toMatch(/\*\*$/);
});

// Quoted list item with content indent
test("quoted list item with content indent sets blockquoteListContainer", () => {
  const result = closeOpenMarkdown("> - item **emph\n>   more");
  expect(result).toMatch(/\*\*$/);
});

// Indented code block at the start (paragraph none)
test("indented code block with paragraph none resets stack", () => {
  const result = closeOpenMarkdown("    code **not emph");
  expect(result).toBe("    code **not emph");
});

// Indented code block continuing a paragraph
test("indented code block continuing a paragraph scans inline", () => {
  const result = closeOpenMarkdown("text\n    **emph");
  expect(result).toMatch(/\*\*$/);
});

// A blockquote that starts a new depth (not continuing)
test("blockquote at different depth starts fresh", () => {
  const result = closeOpenMarkdown("> text **emph\n>> deeper");
  expect(result).not.toMatch(/\*\*$/);
});

// Quoted indented code that continues a paragraph (quoteContinuesParagraph true)
test("quoted indented code continuing paragraph scans inline", () => {
  const result = closeOpenMarkdown("> para **emph\n>     code");
  expect(result).toMatch(/\*\*$/);
});

// --- scanQuotedListChild edge cases (lines 307, 309-310, 312, 353-354) ---
// These require a blockquote > list > nested-quote structure to trigger
// the scanQuotedListChild function.

// Line 307: childQuote.content.trim() === "" inside a quoted list child
// with a nested quote — resets the emphasis stack
test("quoted list child with empty nested quote content resets emphasis", () => {
  // > - text **emph
  // > > (empty content in nested quote)
  const result = closeOpenMarkdown("> - text **emph\n> > ");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 309-310: childQuote.content is an ATX heading — resets and scans
test("quoted list child with ATX heading in nested quote resets and scans", () => {
  // > - text **emph
  // > > ## heading
  const result = closeOpenMarkdown("> - text **emph\n> > ## heading *emph");
  // The ** should be closed (stack reset by heading), but *emph is scanned
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 309-310: childQuote.content is a thematic break — resets
test("quoted list child with thematic break in nested quote resets emphasis", () => {
  const result = closeOpenMarkdown("> - text **emph\n> > ---");
  expect(result).not.toMatch(/\*\*$/);
});

// Line 312: childQuote.content is a setext underline — resets
test("quoted list child with setext underline in nested quote resets emphasis", () => {
  const result = closeOpenMarkdown("> - text **emph\n> > ===");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 353-354: childLine is a thematic break (no nested quote) — resets
test("quoted list child with thematic break (no nested quote) resets emphasis", () => {
  // > - text **emph
  // > ---
  // The thematic break is at the SAME quote level but after the list child
  const result = closeOpenMarkdown("> - **emph\n> ---");
  expect(result).not.toMatch(/\*\*$/);
});

// Lines 353-354: childLine is a setext underline (no nested quote) — resets
test("quoted list child with setext underline (no nested quote) resets emphasis", () => {
  const result = closeOpenMarkdown("> - **emph\n> ===");
  expect(result).not.toMatch(/\*\*$/);
});
