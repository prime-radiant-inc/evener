// Contract test for the shared icon-rail seat (styles/railseat.module.css).
//
// The rail-seat geometry, the ambient strength and the two pulls live in one
// module composed by every consumer, so the declarations are pinned where
// they live and each consumer is pinned to its wiring: compose the shared
// rules, restate nothing. jsdom cannot see CSS, so
// these are declaration-level assertions over the stylesheet sources - the
// same idiom as thinkBlockMotion.test.ts and agentMessageSize.contract.test.ts
// (notificationStatusIcon.contract.test.ts and loader.test.tsx carry the
// consumer-side halves). Comments are stripped first: a stylesheet grep that
// matches its own comment prose asserts nothing.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel: string) => readFileSync(join(here, rel), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

const seat = read("railseat.module.css");
const toolcallitem = read("../panes/session/transcript/toolcallitem.module.css");
const toolrungroup = read("../panes/session/transcript/toolrungroup.module.css");
const notificationcard = read("../panes/session/transcript/messages/notificationcard.module.css");
const loader = read("../widgets/loader/loader.module.css");
const thinkblock = read("../panes/session/transcript/messages/thinkblock.module.css");
const steeringitem = read("../panes/session/transcript/messages/steeringitem.module.css");
const systemnoticeitem = read("../panes/session/transcript/messages/systemnoticeitem.module.css");

// The media query runContent and the rail pull share: the pull only exists
// where the padding it eats exists (turnblock.module.css reserves the gutter
// above this breakpoint). The block matcher runs from the media opener to a
// COLUMN-ZERO closing brace, so an indented inner rule cannot end it early -
// the same gate shape thinkBlockMotion.test.ts and notificationStatusIcon.
// contract.test.ts use.
const MIN_WIDTH_MEDIA = /@media\s*\(min-width:\s*700px\)\s*\{[\s\S]*?\n\}/g;
const seatGated = seat.match(MIN_WIDTH_MEDIA)?.join("\n") ?? "";
const seatUngated = seat.replace(MIN_WIDTH_MEDIA, "");

// A consumer rule's own declarations: the composes lines removed, what is
// left is what the module itself adds on top of the shared seat.
function ownDeclarations(css: string, selector: string): string {
  const body = css.match(new RegExp(`${selector}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
  return body.replace(/composes:[^;]+;/g, "").trim();
}

test("the seat is the avatar column: avatar-size wide, glyph centred, pinned to line 1", () => {
  expect(seat).toMatch(/\.seat\s*\{[^}]*width:\s*var\(--speaker-avatar-size\)/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*justify-content:\s*center/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*align-items:\s*center/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*display:\s*inline-flex/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*align-self:\s*flex-start/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*height:\s*1lh/);
  // font-size matches the rows' first text line: 1lh is the computed
  // line-height of THIS element, so without matching the text's font-size the
  // glyph box is a different height than the line it seats against.
  expect(seat).toMatch(/\.seat\s*\{[^}]*font-size:\s*var\(--font-size-ui\)/);
  expect(seat).toMatch(/\.seat\s*\{[^}]*flex:\s*none/);
});

test("the seat carries the rail ink; the ambient strength is a separate class", () => {
  expect(seat).toMatch(/\.seat\s*\{[^}]*color:\s*var\(--ink-mid\)/);
  // Geometry and strength compose separately: a bare glyph takes the seat
  // without receding.
  expect(seat).not.toMatch(/\.seat\s*\{[^}]*opacity/);
  expect(seat).toMatch(/\.ambient\s*\{[^}]*opacity:\s*0\.5/);
});

test("the seat's text lands at the content edge: speaker-gap net of the row's own gap", () => {
  // The consumer row's --space-2 flex gap also lands between the slot and the
  // text that follows, so the slot's margin-right is the speaker-gap MINUS
  // that gap - the arithmetic that seats every rail row's text on the same
  // content edge (the layoutguard case transcript-rail-icon-seats measures
  // the 10px slot-to-text gap it produces).
  expect(seat).toMatch(/\.seat\s*\{[^}]*margin-right:\s*calc\(var\(--speaker-gap\) - var\(--space-2\)\)/);
});

test("the gutter pull exists only above the breakpoint; no separate internal column pull exists", () => {
  expect(seatGated).toMatch(/\.gutterPull\s*\{[^}]*margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/);
  // Below 700px the turn's gutter collapses, so the gutter pull has no
  // reserved padding to eat there: it must not exist outside the media block.
  expect(seatUngated).not.toMatch(/\.gutterPull/);
  expect(seat).not.toMatch(/\.columnPull/);
});

test("ToolRow's kind icon composes the seat without the ambient strength; its slot composes the gutter pull", () => {
  expect(toolcallitem).toMatch(
    /\.rowIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  // A tool row's glyph sits bare in the rail at full strength and restates no
  // geometry of its own.
  expect(toolcallitem).not.toMatch(/\.rowIcon\s*\{[^}]*composes:\s*ambient/);
  // The pull rides the row's own flex item (the icon's slot, which is also the
  // failure badge's positioning context): the breakpoint-gated gutter pull
  // into .runContent / .intentGroupItems, with no double-indented .call inset.
  expect(toolcallitem).toMatch(
    /\.rowIconSlot\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(toolcallitem).toMatch(/\.call\s*\{[^}]*padding:\s*var\(--tool-card-row-pad\)\s+0/);
  expect(toolcallitem).not.toMatch(/padding-left:\s*calc\(var\(--tool-card-inset\) \+ var\(--speaker-gutter\)\)/);
  // The one local rule that is genuinely ToolRow's own: on intent-bearing rows
  // the first line is the rationale, whose line-height is --line-height-title
  // (tighter than the body's), so the 1lh box must narrow with it.
  expect(toolcallitem).toMatch(
    /\.row\[data-intent="true"\] \.rowIcon\s*\{[^}]*line-height:\s*var\(--line-height-title\)/,
  );
});

test("ToolRunGroup's leading chevron composes the seat and gutter pull with flush horizontal padding", () => {
  expect(toolrungroup).toMatch(
    /\.railChevron\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(toolrungroup).toMatch(
    /\.railChevron\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(toolrungroup).toMatch(/\.summary\s*\{[^}]*padding:\s*var\(--tool-card-row-pad\)\s+0/);
});

test("both static and collapsible notification heads share statusIcon (seat, ambient, gutterPull) with no summaryIcon split", () => {
  expect(notificationcard).toMatch(
    /\.statusIcon\s*\{[^}]*composes:\s*ambient from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(notificationcard).toMatch(
    /\.statusIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(notificationcard).toMatch(
    /\.statusIcon\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(ownDeclarations(notificationcard, "\\.statusIcon")).toBe("");
  expect(notificationcard).not.toMatch(/\.summaryIcon/);
});

test("the Loader rail row composes the pull only: its grid slot is a grid, not a glyph box", () => {
  // The rail row's own slot is a 3x3 cell grid inside a --speaker-avatar-size
  // column, not a 1lh glyph box, and the pull rides the row root, not the
  // slot - so .rail composes the pull alone and keeps its slot local.
  expect(loader).toMatch(/\.rail\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/styles\/railseat\.module\.css"/);
  expect(ownDeclarations(loader, "\\.rail")).toBe("");
  expect(loader).toMatch(/\.rail \.grid\s*\{[^}]*width:\s*var\(--speaker-avatar-size\)/);
  expect(loader).toMatch(/\.rail \.grid\s*\{[^}]*justify-content:\s*center/);
  expect(loader).toMatch(/\.rail \.grid\s*\{[^}]*margin-right:\s*calc\(var\(--speaker-gap\) - var\(--space-2\)\)/);
});

test("the live thought's kind glyph stays the single-line variant while the settled summary bulb composes the shared rail seat", () => {
  const iconRule = thinkblock.match(/\.icon\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(iconRule).not.toMatch(/composes:/);
  expect(iconRule).toMatch(/align-self:\s*center/);
  expect(iconRule).toMatch(/margin-right:\s*var\(--speaker-gap\)/);

  expect(thinkblock).toMatch(
    /\.summaryIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(thinkblock).toMatch(
    /\.summaryIcon\s*\{[^}]*composes:\s*ambient from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(thinkblock).toMatch(
    /\.summaryIcon\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(ownDeclarations(thinkblock, "\\.summaryIcon")).toBe("font-size: var(--font-size-body);");
});

test("the steer diamond composes the shared rail seat, ambient strength, and gutter pull", () => {
  expect(steeringitem).toMatch(
    /\.railIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(steeringitem).toMatch(
    /\.railIcon\s*\{[^}]*composes:\s*ambient from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(steeringitem).toMatch(
    /\.railIcon\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(ownDeclarations(steeringitem, "\\.railIcon")).toBe("font-size: var(--font-size-body);");
});

test("the turn failure glyph composes the shared rail seat and gutter pull at full strength", () => {
  expect(systemnoticeitem).toMatch(
    /\.failureIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(systemnoticeitem).toMatch(
    /\.failureIcon\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(systemnoticeitem).not.toMatch(/\.failureIcon\s*\{[^}]*composes:\s*ambient/);
  expect(ownDeclarations(systemnoticeitem, "\\.failureIcon")).toBe("");
});
