import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "../../../styles/cssBlock";

// The entity card's internals follow the design system's card vocabulary, not
// the inverted tooltip palette the card used to inherit from its bubble: the
// status reads color-by-meaning (failed = danger, running = alive, terminal
// states stay on the ink ramp - the hue law reserves green for ACTIVE work, so
// a completed job is a fact, not a success signal). Hierarchy comes from
// the ink ramp, never from opacity fades. The tooltip-palette non-leak law
// itself lives centrally in token-contract.test.ts (§b2).
const CSS = readModuleCss(import.meta.url, "entityref.module.css");

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
  // Quiet captions drop all the way to the placeholder step, one rung below
  // the readable secondary metadata in the card's PropertyList labels.
  expect(topRuleBlock(CSS, ".caption")).toMatch(/color:\s*var\(--ink-low\)/);
});

test("no opacity fades fake the hierarchy", () => {
  expect(CSS).not.toMatch(/(^|\s)opacity:/);
});

// A group the line cannot fit - a long job id at the question's pane-title
// prose size on a 390px phone - must wrap inside its row instead of painting
// past the transcript's clip and leaving the open control untappable: the id
// breaks like any other long token (the same escape table cells get, where
// the column owns the width), and a group that fits a full line still moves
// there as one unit because anywhere only breaks when nothing else fits.
test("a group wider than its line wraps like a long token instead of clipping", () => {
  const group = topRuleBlock(CSS, ".group");
  expect(group).toMatch(/white-space:\s*normal/);
  expect(group).toMatch(/max-width:\s*100%/);
  expect(group).toMatch(/overflow-wrap:\s*anywhere/);
});
