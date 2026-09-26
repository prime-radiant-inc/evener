// @vitest-environment node

import { expect, test } from "vitest";
import { mediaBlock, readModuleCss, topRuleBlock } from "../../styles/cssBlock";

// The hover card is a floating layer of the design system, not an oversized
// tooltip. Every other overlay (popover, menu, dialog, sheet) sits on
// --surface-1 with --shadow-overlay supplying both the 1px strong-edge ring
// and the soft shadow, and the design system forbids doubling that ring with
// a second border (docs/web-ui/design-system.md §2). The tooltip keeps its
// inverted mini-palette precisely because a one-line label is not a card, so
// the card must not borrow it either. This pins the bubble to the overlay
// family so the card can never quietly slide back into the tooltip's clothes;
// the tooltip-palette non-leak law itself lives centrally in
// token-contract.test.ts (§b2). The CSS declarations are read straight off
// disk because vitest leaves .module.css imports unprocessed (test.css
// defaults to false), so no rendered-DOM assert could ever see them.
const CSS = readModuleCss(import.meta.url, "hovercard.module.css");

// --- the contract ----------------------------------------------------------

test("the bubble rides the overlay surface family, not the tooltip mini-palette", () => {
  const bubble = topRuleBlock(CSS, ".bubble");
  expect(bubble).toMatch(/background:\s*var\(--surface-1\)/);
  expect(bubble).toMatch(/box-shadow:\s*var\(--shadow-overlay\)/);
  expect(bubble).toMatch(/border-radius:\s*var\(--radius-pane\)/);
  expect(bubble).toMatch(/color:\s*var\(--ink-hi\)/);
  // The overlay shadow's 1px ring is the boundary; a second border would
  // double it, which §2 forbids.
  expect(bubble).not.toMatch(/(^|\s)border:/);
  // A card is denser than a one-line label but still chrome: the ui step, not
  // the caption step, and never the reading-prose step.
  expect(bubble).toMatch(/font-size:\s*var\(--font-size-ui\)/);
  // Still tooltip-classed in stacking: it must beat menus.
  expect(bubble).toMatch(/z-index:\s*var\(--z-tooltip\)/);
});

test("the bubble fades and scales in on the shared overlay motion budget", () => {
  const bubble = topRuleBlock(CSS, ".bubble");
  expect(bubble).toMatch(
    /animation:\s*hovercardFadeScale\s+var\(--motion-duration-overlay\)\s+var\(--motion-easing-standard\)/,
  );
  expect(CSS).toMatch(/@keyframes hovercardFadeScale\s*\{/);
  // The same off-ramp popover carries: no motion under prefers-reduced-motion.
  expect(mediaBlock(CSS, "prefers-reduced-motion: reduce")).toMatch(/animation:\s*none/);
});

test("the bubble stays hidden on hoverless devices", () => {
  expect(mediaBlock(CSS, "hover: none")).toMatch(/display:\s*none/);
});
