import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Loader } from "./index";
import styles from "./loader.module.css";

// One read of the CSS module's source, shared by every declaration-level
// test below (the same module-scope read notificationStatusIcon.contract.
// test.ts uses for its module).
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "loader.module.css"), "utf8");

// Splits the stylesheet at ONE media query into what sits inside its block
// and what sits outside it, by counting braces from the block's opening one
// (nesting included). Throws when the query is absent, so a deleted gate
// fails the tests that depend on it instead of silently passing them.
function mediaBlock(query: string): { inside: string; outside: string } {
  const mediaStart = css.indexOf(query);
  if (mediaStart === -1) throw new Error(`no such media query in loader.module.css: ${query}`);
  const blockOpen = css.indexOf("{", mediaStart);
  let depth = 0;
  let blockEnd = -1;
  for (let i = blockOpen; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) {
        blockEnd = i;
        break;
      }
    }
  }
  if (blockEnd === -1) throw new Error(`unbalanced braces in loader.module.css from ${query}`);
  return {
    inside: css.slice(blockOpen, blockEnd),
    outside: css.slice(0, mediaStart) + css.slice(blockEnd + 1),
  };
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("always renders a 9-cell pixel grid", () => {
  render(<Loader />);
  expect(screen.getAllByTestId("loader-cell")).toHaveLength(9);
});

test("the grid is decorative (hidden from assistive tech)", () => {
  render(<Loader />);
  expect(screen.getByTestId("loader-grid").getAttribute("aria-hidden")).toBe("true");
});

test("announces itself as loading by default, for assistive tech", () => {
  render(<Loader />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("uses the label as its accessible name when given", () => {
  render(<Loader label="Starting agent" />);
  expect(screen.getByRole("status", { name: "Starting agent" })).toBeTruthy();
});

test("renders the label text visibly when given", () => {
  render(<Loader label="Starting agent" />);
  expect(screen.getByText("Starting agent")).toBeTruthy();
});

test("renders no label text when not given", () => {
  render(<Loader />);
  expect(screen.queryByTestId("loader-label")).toBeNull();
});

test("renders no elapsed readout when startedAt/now are not given", () => {
  render(<Loader />);
  expect(screen.queryByTestId("loader-elapsed")).toBeNull();
});

test("renders no elapsed readout when only one of startedAt/now is given", () => {
  render(<Loader startedAt={1_000} />);
  expect(screen.queryByTestId("loader-elapsed")).toBeNull();
  render(<Loader now={1_000} />);
  expect(screen.queryAllByTestId("loader-elapsed")).toHaveLength(0);
});

test("renders elapsed mm:ss when startedAt and now are both given", () => {
  render(<Loader startedAt={0} now={5_000} />);
  expect(screen.getByTestId("loader-elapsed").textContent).toBe("0:05");
});

test("elapsed rolls over into minutes past 60s", () => {
  render(<Loader startedAt={0} now={65_000} />);
  expect(screen.getByTestId("loader-elapsed").textContent).toBe("1:05");
});

test("elapsed seconds are zero-padded under 10", () => {
  render(<Loader startedAt={0} now={61_000} />);
  expect(screen.getByTestId("loader-elapsed").textContent).toBe("1:01");
});

test("elapsed clamps to 0:00 when now precedes startedAt (clock-skew guard)", () => {
  render(<Loader startedAt={5_000} now={0} />);
  expect(screen.getByTestId("loader-elapsed").textContent).toBe("0:00");
});

test("never re-renders on its own - no internal timers (now is fully prop-driven)", () => {
  vi.useFakeTimers();
  render(<Loader startedAt={0} now={5_000} />);
  const before = screen.getByTestId("loader-elapsed").textContent;
  vi.advanceTimersByTime(120_000); // 2 minutes of virtual time; nothing is scheduled
  const after = screen.getByTestId("loader-elapsed").textContent;
  expect(after).toEqual(before);
});

// Motion law (Direction, Global Constraints): idle animation is banned, and
// Cadence's honest-liveness stance means agent liveness is never faked with
// motion. Loader's grid animation is the one deliberate exception - reserved
// for genuinely indeterminate user-initiated waits - and even then it must
// stay opt-in behind prefers-reduced-motion: no-preference, never running
// unconditionally. A real browser engine isn't available in jsdom to assert
// "this only animates under that media query" any other way, so this reads
// the CSS module's own source and checks every animation/@keyframes
// declaration is nested inside that one media block, the same source-reading
// technique token-contract.test.ts and skeleton.test.tsx already use.
test("its CSS module gates all animation behind prefers-reduced-motion: no-preference", () => {
  const { inside, outside } = mediaBlock("@media (prefers-reduced-motion: no-preference)");
  expect(outside).not.toMatch(/@keyframes|animation\s*:/);
  expect(inside).toMatch(/@keyframes|animation\s*:/);
});

// --- rail mode: the transcript's icon-rail seating ---------------------------
// The content-free "Thinking…" row (ThinkBlock) seats the pulsing grid in the
// same rail column the live/settled rows seat their bulb glyph in. jsdom
// cannot measure the seat, so the geometry is pinned at the declaration
// level - the same source-reading technique as the motion test above.

test("rail sets the variant class on the row; the default loader does not", () => {
  const { unmount } = render(<Loader label="Thinking…" rail />);
  expect(screen.getByRole("status").className).toContain(styles.rail);
  unmount();
  render(<Loader label="Thinking…" />);
  expect(screen.getByRole("status").className).not.toContain(styles.rail);
});

test("rail seats the grid in the standard icon slot: avatar-size, centred, speaker-gap net of the row gap", () => {
  // The slot is the avatar column, and the 15px grid centres inside it.
  expect(css).toMatch(/\.rail \.grid\s*\{[^}]*width:\s*var\(--speaker-avatar-size\)/);
  expect(css).toMatch(/\.rail \.grid\s*\{[^}]*justify-content:\s*center/);
  // The row's own --space-2 gap also lands between the grid and the label,
  // so the grid's margin-right is the speaker-gap MINUS that gap - the same
  // arithmetic ToolRow's .rowIcon uses to seat its row's text at the content
  // edge (see toolcallitem.module.css).
  expect(css).toMatch(/\.rail \.grid\s*\{[^}]*margin-right:\s*calc\(var\(--speaker-gap\) - var\(--space-2\)\)/);
});

test("the rail row composes the shared gutter pull rather than restating it", () => {
  // The pull lives in the shared rail seat (styles/railseat.module.css) so
  // the three rail pulls cannot drift apart; this module keeps no copy of
  // it. The gate around the shared .gutterPull is pinned where the shared
  // geometry lives, in railseat.contract.test.ts; a local restatement is
  // what this test forbids.
  expect(css).toMatch(/\.rail\s*\{[^}]*composes:\s*gutterPull from "\.\.\/\.\.\/styles\/railseat\.module\.css"/);
  expect(css).not.toMatch(/@media\s*\(min-width:\s*700px\)/);
  expect(css).not.toMatch(/margin-left:\s*calc\(-1/);
});
