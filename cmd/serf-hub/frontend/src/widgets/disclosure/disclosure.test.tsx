import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "./disclosureStore";
import { Disclosure } from "./index";

// jsdom runs no animations, so A6's motion can only be asserted at the
// declaration level. Comments are stripped FIRST: a stylesheet grep that
// matches its own comment prose asserts nothing (this repo has that precedent).
function motionCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "disclosure.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

test("starts collapsed by default; clicking the summary expands", () => {
  render(
    <Disclosure id="d1" summary="Head" data-testid="d">
      Body
    </Disclosure>,
  );
  expect(screen.queryByText("Body")).toBeNull();
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
});

test("open state survives remount because it lives in the store", () => {
  const { unmount } = render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
  unmount();
  render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy(); // still open after remount
});

test("defaultOpen renders open when the store has no entry", () => {
  render(
    <Disclosure id="d2" summary="Head" defaultOpen>
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy();
});

// --- A6: opening animates, subtly, and honors prefers-reduced-motion -----

test("the chevron rotation and the body fade are declared with real motion", () => {
  const css = motionCss();
  expect(css).toMatch(/\.chevron\s*\{[^}]*transition:\s*transform/);
  expect(css).toMatch(/\.body\s*\{[^}]*animation:\s*disclosure-body-in/);
});

test("every motion declaration uses an existing motion token - no invented duration", () => {
  const css = motionCss();
  const durations = css.match(/(?:transition|animation):[^;]*?(\d+m?s)/g);
  expect(durations).toBe(null); // no literal duration anywhere
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toContain("var(--motion-easing-standard)");
});

test("all motion sits inside a prefers-reduced-motion: no-preference gate", () => {
  const css = motionCss();
  // Every transition/animation declaration must live in the gated block, so a
  // reader who asked for less motion gets an instant open.
  const gated = /@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(gated).not.toBeNull();
  const outsideGate = css.replace(gated![0], "");
  expect(outsideGate).not.toMatch(/\btransition:/);
  expect(outsideGate).not.toMatch(/\banimation:/);
});

test("the motion is a fade, not a slide or a bounce", () => {
  const css = motionCss();
  const keyframes = /@keyframes\s+disclosure-body-in\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(keyframes).not.toBeNull();
  expect(keyframes![1]).toContain("opacity");
  expect(keyframes![1]).not.toContain("translate");
  expect(keyframes![1]).not.toContain("scale");
});
