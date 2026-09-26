import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "../../../styles/cssBlock";

// The entity card's internals follow the design system's card vocabulary, not
// the inverted tooltip palette the card used to inherit from its bubble: the
// kind label is the one eyebrow recipe (docs/web-ui/design-system.md §2), the
// status reads color-by-meaning (failed = danger, running = alive, terminal
// states stay on the ink ramp - the hue law reserves green for ACTIVE work, so
// a completed job is a fact, not a success signal), and the meta rows follow
// the meta-table idiom (ink-mid labels, ink-hi values). Hierarchy comes from
// the ink ramp, never from opacity fades. The tooltip-palette non-leak law
// itself lives centrally in token-contract.test.ts (§b2).
const CSS = readModuleCss(import.meta.url, "entityref.module.css");

test("the kind label is the eyebrow recipe, one container label inside the card", () => {
  const kind = topRuleBlock(CSS, ".kind");
  expect(kind).toMatch(/font-size:\s*var\(--font-size-caption\)/);
  expect(kind).toMatch(/font-weight:\s*var\(--font-weight-semibold\)/);
  expect(kind).toMatch(/text-transform:\s*uppercase/);
  expect(kind).toMatch(/letter-spacing:\s*var\(--tracking-eyebrow\)/);
  expect(kind).toMatch(/color:\s*var\(--ink-mid\)/);
});

test("status reads color-by-meaning: failure is danger, live work is alive, everything else is ink", () => {
  expect(topRuleBlock(CSS, ".status")).toMatch(/color:\s*var\(--ink-mid\)/);
  expect(topRuleBlock(CSS, '.status[data-state="failed"]')).toMatch(/color:\s*var\(--danger-ink\)/);
  expect(topRuleBlock(CSS, '.status[data-state="running"]')).toMatch(/color:\s*var\(--alive-ink\)/);
  // The complete set of hue-carrying states, exhaustively: done is NOT alive (a
  // completed job is a neutral fact), and no future state can sneak a hue in
  // unreviewed (design-system §1 "Color is meaning"). Order-insensitive: the
  // two rules' file order carries no CSS meaning (equal specificity, distinct
  // attribute values), so the test must not fail on a reorder.
  const hueStates = [...CSS.matchAll(/\.status\[data-state="([^"]+)"\]/g)].map((match) => match[1]);
  expect([...hueStates].sort()).toEqual(["failed", "running"]);
});

test("the card's hierarchy comes from the ink ramp", () => {
  expect(topRuleBlock(CSS, ".summary")).toMatch(/color:\s*var\(--ink-hi\)/);
  expect(topRuleBlock(CSS, ".key")).toMatch(/color:\s*var\(--ink-mid\)/);
  expect(topRuleBlock(CSS, ".value")).toMatch(/color:\s*var\(--ink-hi\)/);
  // Quiet captions drop all the way to the placeholder step, one rung below
  // the readable secondary metadata in .key.
  expect(topRuleBlock(CSS, ".caption")).toMatch(/color:\s*var\(--ink-low\)/);
});

test("no opacity fades fake the hierarchy", () => {
  expect(CSS).not.toMatch(/(^|\s)opacity:/);
});
