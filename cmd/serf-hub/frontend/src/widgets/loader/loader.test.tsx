import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Loader } from "./index";

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
  render(<Loader label="Spawning agent" />);
  expect(screen.getByRole("status", { name: "Spawning agent" })).toBeTruthy();
});

test("renders the label text visibly when given", () => {
  render(<Loader label="Spawning agent" />);
  expect(screen.getByText("Spawning agent")).toBeTruthy();
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
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "loader.module.css"), "utf8");

  const mediaStart = css.indexOf("@media (prefers-reduced-motion: no-preference)");
  expect(mediaStart).toBeGreaterThan(-1);

  // Find the media block's matching closing brace by counting braces from
  // its opening one.
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
  expect(blockEnd).toBeGreaterThan(-1);

  const outsideMediaBlock = css.slice(0, mediaStart) + css.slice(blockEnd + 1);
  expect(outsideMediaBlock).not.toMatch(/@keyframes|animation\s*:/);

  const insideMediaBlock = css.slice(blockOpen, blockEnd);
  expect(insideMediaBlock).toMatch(/@keyframes|animation\s*:/);
});
