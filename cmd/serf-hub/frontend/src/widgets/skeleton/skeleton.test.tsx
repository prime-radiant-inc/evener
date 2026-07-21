import { afterEach, test, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { Skeleton } from "./index";

afterEach(cleanup);

test("defaults to 3 placeholder lines", () => {
  render(<Skeleton />);
  expect(screen.getAllByTestId("skeleton-line")).toHaveLength(3);
});

test("renders the requested number of placeholder lines", () => {
  render(<Skeleton lines={5} />);
  expect(screen.getAllByTestId("skeleton-line")).toHaveLength(5);
});

test("renders a single line when asked for one", () => {
  render(<Skeleton lines={1} />);
  expect(screen.getAllByTestId("skeleton-line")).toHaveLength(1);
});

test("announces itself as loading, for assistive tech", () => {
  render(<Skeleton />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("individual lines are decorative (hidden from assistive tech)", () => {
  render(<Skeleton lines={2} />);
  for (const line of screen.getAllByTestId("skeleton-line")) {
    expect(line.getAttribute("aria-hidden")).toBe("true");
  }
});

// Honest-liveness rule (Direction, Global Constraints): a skeleton is
// static, never a shimmer/pulse loop, so a stalled load reads honestly
// instead of faking motion it isn't making. Enforced here by reading the
// CSS module's own source, the same way token-contract.test.ts and
// button.test.tsx's focus-visible test do - a real browser rendering
// engine isn't available in jsdom to assert "nothing is animating" any
// other way.
test("its CSS module declares no animation, keyframes, or transition - static only", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "skeleton.module.css"), "utf8");
  expect(css).not.toMatch(/@keyframes|animation\s*:|transition\s*:/);
});
