// @vitest-environment node

// Declaration-level checks on thinkblock.module.css. jsdom runs no animations
// or cascade, so these assert declarations. Comments are stripped FIRST: a
// stylesheet grep that matches its own comment prose asserts nothing (this
// repo has that precedent).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

function css(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "thinkblock.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

// The settled thought is a Disclosure: the chevron turn and the body fade are
// the widget's (widgets/disclosure), so the thought must not stack a second
// open animation on top of them.
test("the settled thought declares no open motion of its own", () => {
  const text = css();
  expect(text).not.toMatch(/\banimation:/);
  expect(text).not.toMatch(/\btransition:/);
  expect(text).not.toMatch(/@keyframes/);
});

// Not motion, but the same declaration-level style of check: thinking is body
// text like the turns around it (Jesse's review call) - quiet through INK,
// not size. The live "Thinking…" eyebrow renders at --font-size-body (mockup
// #4: a caption-sized label was quiet through size, which the design law
// bans), and the settled summary inherits the transcript's body step through
// the Disclosure widget. The live and settled BODIES both render through the
// Markdown widget, whose root composes with its host (1em fallback, see
// widgets/markdown/markdown.module.css), so both inherit the ambient body
// step - neither body may re-declare a size - quiet there comes from
// --markdown-ink alone.
test("settled and live thinking render at body size, not caption/ui", () => {
  const text = css();
  expect(text).toMatch(/\.label\s*\{[^}]*font-size:\s*var\(--font-size-body\)/);
  expect(text).not.toMatch(/\.label\s*\{[^}]*font-size:\s*var\(--font-size-caption\)/);
  expect(text).not.toMatch(/\.(summaryText|liveBody|body)\s*\{[^}]*font-size:/);
});
