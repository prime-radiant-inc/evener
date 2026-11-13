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
const notificationcard = read("../panes/session/transcript/messages/notificationcard.module.css");
const loader = read("../widgets/loader/loader.module.css");
const thinkblock = read("../panes/session/transcript/messages/thinkblock.module.css");
const steeringitem = read("../panes/session/transcript/messages/steeringitem.module.css");

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
  // Geometry and strength compose separately: a glyph that sits on its own
  // ground takes the seat without receding.
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

test("the gutter pull exists only above the breakpoint; the column pull holds at every width", () => {
  expect(seatGated).toMatch(/\.gutterPull\s*\{[^}]*margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/);
  // Below 700px the turn's gutter collapses, so the gutter pull has no
  // reserved padding to eat there: it must not exist outside the media block.
  expect(seatUngated).not.toMatch(/\.gutterPull/);
  // The column pull is for a row that reserves the column in its own padding,
  // which exists at every width.
  expect(seatUngated).toMatch(/\.columnPull\s*\{[^}]*margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/);
  expect(seatGated).not.toMatch(/\.columnPull/);
});

test("ToolRow's kind icon composes the seat without the ambient strength; its slot composes the column pull", () => {
  expect(toolcallitem).toMatch(
    /\.rowIcon\s*\{[^}]*composes:\s*seat from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  // A tool row's glyph sits on its own --field ground inside a card, which
  // does the receding the rail's half opacity does, so the row takes the
  // seat's geometry and leaves the ambient strength uncomposed. Its one own
  // declaration keeps the glyph painted above that ground; it restates no
  // geometry and overrides nothing the seat declares.
  expect(toolcallitem).not.toMatch(/\.rowIcon\s*\{[^}]*composes:\s*ambient/);
  expect(ownDeclarations(toolcallitem, "\\.rowIcon")).toBe("position: relative;");
  // The pull rides the row's own flex item (the icon's slot, which is also the
  // failure badge's positioning context): the ungated column pull, since a
  // tool row reserves its seat column in its own padding.
  expect(toolcallitem).toMatch(
    /\.rowIconSlot\s*\{[^}]*composes:\s*columnPull from "\.\.\/\.\.\/\.\.\/styles\/railseat\.module\.css"/,
  );
  expect(toolcallitem).toMatch(
    /\.call\s*\{[^}]*padding-left:\s*calc\(var\(--tool-card-inset\) \+ var\(--speaker-gutter\)\)/,
  );
  // The one local rule that is genuinely ToolRow's own: on intent-bearing rows
  // the first line is the rationale, whose line-height is --line-height-title
  // (tighter than the body's), so the 1lh box must narrow with it.
  expect(toolcallitem).toMatch(
    /\.row\[data-intent="true"\] \.rowIcon\s*\{[^}]*line-height:\s*var\(--line-height-title\)/,
  );
});

test("the notification head's status glyph composes the seat, the ambient strength and the gutter pull", () => {
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

test("the live thought's kind glyph stays the deliberate variant, not a seat", () => {
  // ThinkBlock's .icon shares the rail COLUMN but not the seat: the label row
  // is single-line (no 1lh pinning, no font-size matching - the bulb is a
  // fixed 14px stroke), .label centers it (align-self: center, not
  // flex-start), and its margin-right is the FULL speaker gap because the
  // label flex row has no column gap to subtract. The pull rides the
  // containers instead of the glyph (thinkblock.module.css documents why:
  // .summary's overflow: hidden would clip a negatively-margined icon), so
  // .gutterPull is not composed here either. The divergence is a decision,
  // not drift - pinned so a future "compose the seat" cleanup has to argue
  // with this test, not silently change the thought row's seating.
  const iconRule = thinkblock.match(/\.icon\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(iconRule).not.toMatch(/composes:/);
  expect(iconRule).toMatch(/align-self:\s*center/);
  expect(iconRule).toMatch(/margin-right:\s*var\(--speaker-gap\)/);
});

test("the steer diamond is not a seat: a Disclosure summary glyph in the content column", () => {
  // SteeringDivider is a Disclosure, and a Disclosure summary sits in the
  // content column like every other one, so the diamond composes nothing
  // from the rail seat and nothing in the stylesheet pulls into the gutter.
  const diamondRule = steeringitem.match(/\.railIcon\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(diamondRule).not.toMatch(/composes:/);
  expect(diamondRule).toMatch(/opacity:\s*0\.5/);
  expect(steeringitem).not.toMatch(/margin-left:\s*calc\(-1 \* var\(--speaker-gutter\)\)/);
});
