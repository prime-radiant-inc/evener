// @vitest-environment node

// A6's motion check for the thinking block's own disclosure. It lives in its
// own file rather than in ThinkBlock.test.tsx because that file (and
// ThinkBlock.tsx) belong to a concurrent stream; this task's change is confined
// to thinkblock.module.css, so its assertion is too.
//
// jsdom runs no animations, so this asserts declarations. Comments are stripped
// FIRST: a stylesheet grep that matches its own comment prose asserts nothing
// (this repo has that precedent).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

function css(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "thinkblock.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

test("the thinking block's body fades in when opened", () => {
  expect(css()).toMatch(/animation:\s*thinkblock-body-in/);
});

test("the animation is scoped to the OPEN details, so a collapsed block never animates", () => {
  expect(css()).toMatch(/\.details\[open\]\s*>\s*\.body/);
});

test("it uses only existing motion tokens - no invented duration", () => {
  const text = css();
  expect(text.match(/(?:transition|animation):[^;]*?\d+m?s/g)).toBe(null);
  expect(text).toContain("var(--motion-duration-overlay)");
  expect(text).toContain("var(--motion-easing-standard)");
});

test("the motion sits behind a prefers-reduced-motion gate", () => {
  const text = css();
  const gate = /@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{[\s\S]*?\n\}/.exec(text);
  expect(gate).not.toBeNull();
  expect(text.replace(gate![0], "")).not.toMatch(/\banimation:/);
});

test("the motion is a fade, not a slide or a bounce", () => {
  const keyframes = /@keyframes\s+thinkblock-body-in\s*\{([\s\S]*?)\n\}/.exec(css());
  expect(keyframes).not.toBeNull();
  expect(keyframes![1]).toContain("opacity");
  expect(keyframes![1]).not.toContain("translate");
  expect(keyframes![1]).not.toContain("scale");
});

// Not motion, but the same declaration-level style of check: thinking is body
// text like the turns around it (Jesse's review call) - quiet through INK,
// not size. Both the settled "Thought · preview" summary and the live
// streaming paragraph render at --font-size-body.
test("settled and live thinking render at body size, not caption/ui", () => {
  const text = css();
  expect(text).toMatch(/\.summary\s*\{[^}]*font-size:\s*var\(--font-size-body\)/);
  expect(text).toMatch(/\.paragraph\s*\{[^}]*font-size:\s*var\(--font-size-body\)/);
  expect(text).not.toMatch(/\.summary\s*\{[^}]*font-size:\s*var\(--font-size-caption\)/);
  expect(text).not.toMatch(/\.paragraph\s*\{[^}]*font-size:\s*var\(--font-size-ui\)/);
});
