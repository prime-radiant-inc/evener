// Contract test for the notification card's status glyph seat. The card is a
// "run" row (layoutRoles.ts), so its head sits inside .runContent's reserved
// --speaker-gutter padding above the 700px breakpoint; the status glyph is
// the one element that reaches back into that gutter, in the same seat every
// other rail icon (ToolRow's .rowIcon, ThinkBlock's bulb, the steer diamond)
// occupies. jsdom cannot see CSS, so these are declaration-level assertions
// over notificationcard.module.css - the same idiom as thinkBlockMotion.test.ts
// and agentMessageSize.contract.test.ts. Comments are stripped first: a
// stylesheet grep that matches its own comment prose asserts nothing.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "notificationcard.module.css"), "utf8");
const uncommented = css.replace(/\/\*[\s\S]*?\*\//g, "");

// The media query runContent and the rail pull share: the pull only exists
// where the padding it eats exists. toolcallitem.module.css documents the same
// coupling for .rowIcon ("this rule and the indent share one media query").
// The block matcher runs from the media opener to a COLUMN-ZERO closing
// brace, so an indented inner rule cannot end it early - the same gate shape
// thinkBlockMotion.test.ts uses for its reduced-motion gate.
const MIN_WIDTH_MEDIA = /@media\s*\(min-width:\s*700px\)\s*\{[\s\S]*?\n\}/g;
const gated = uncommented.match(MIN_WIDTH_MEDIA)?.join("\n") ?? "";
const ungated = uncommented.replace(MIN_WIDTH_MEDIA, "");

test("the status slot is the avatar column: avatar-size wide, glyph centred, pinned to line 1", () => {
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*width:\s*var\(--speaker-avatar-size\)/);
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*justify-content:\s*center/);
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*align-self:\s*flex-start/);
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*height:\s*1lh/);
});

test("the status glyph is ambient context, not content: half opacity in the rail ink", () => {
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*opacity:\s*0\.5/);
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*color:\s*var\(--ink-mid\)/);
});

test("the head's text seats at the content edge: speaker-gap net of the head's own gap", () => {
  // The head's --space-2 flex gap also lands between the slot and the text,
  // so the slot's margin-right is the speaker-gap MINUS that gap - the same
  // arithmetic .rowIcon uses (toolcallitem.module.css).
  expect(uncommented).toMatch(/\.statusIcon\s*\{[^}]*margin-right:\s*calc\(var\(--speaker-gap\) - var\(--space-2\)\)/);
});

test("the gutter pull exists only above the breakpoint, never unconditionally", () => {
  expect(gated).toMatch(/\.statusIcon\s*\{[^}]*margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/);
  // Outside the media blocks the pull must not appear at all: below 700px
  // there is no reserved padding to eat, and an unconditional negative margin
  // would push the glyph out of the pane.
  expect(ungated).not.toMatch(/\.statusIcon\s*\{[^}]*margin-left:\s*calc\(-1/);
});
