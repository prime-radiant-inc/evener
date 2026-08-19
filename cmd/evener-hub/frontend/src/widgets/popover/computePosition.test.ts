import { expect, test } from "vitest";
import { clampIntoViewport, computePopoverPosition, EDGE_MARGIN, resolveAxis, TRIGGER_GAP } from "./computePosition";

// clampIntoViewport is the shift primitive resolveAxis falls back to and the
// whole of widgets/tooltip's horizontal collision handling - see
// widgets/tooltip/computePosition.ts for why the tooltip needs a shift where
// the popover needs a flip.
test("clampIntoViewport leaves a box that already fits where it is", () => {
  expect(clampIntoViewport(100, 80, 1000)).toBe(100);
});
test("clampIntoViewport slides a box back from the far edge, keeping EDGE_MARGIN", () => {
  // 960 + 80 would end at 1040, past both the viewport and its margin.
  expect(clampIntoViewport(960, 80, 1000)).toBe(1000 - 80 - EDGE_MARGIN);
});
test("clampIntoViewport slides a box back from the near edge, keeping EDGE_MARGIN", () => {
  expect(clampIntoViewport(-40, 80, 1000)).toBe(EDGE_MARGIN);
});
test("a box too wide for both margins pins to the near edge and overhangs the far one", () => {
  // 240 + 2*8 = 256 > 250, so one margin has to give. The near edge wins:
  // overhanging the far margin costs reserved clear space; violating the near
  // one would push the box's leading edge off-screen.
  expect(clampIntoViewport(20, 240, 250)).toBe(EDGE_MARGIN);
});

test("resolveAxis returns primary when it fits", () => {
  expect(resolveAxis(10, 500, 100, 1000)).toBe(10);
});
test("resolveAxis flips when primary overflows and flipped fits", () => {
  // primary 950 + size 100 = 1050 > 1000 - 8; flipped 40 >= 8 -> flip
  expect(resolveAxis(950, 40, 100, 1000)).toBe(40);
});
test("resolveAxis clamps when neither side fits", () => {
  // primary 950 overflows; flipped -5 < 8 -> clamp into [8, 1000-100-8=892]
  expect(resolveAxis(950, -5, 100, 1000)).toBe(892);
});
test("computePopoverPosition opens below the trigger by TRIGGER_GAP", () => {
  const rect = { left: 20, right: 120, top: 30, bottom: 50 } as DOMRect;
  const pos = computePopoverPosition(rect, { width: 80, height: 40 });
  expect(pos.top).toBe(50 + TRIGGER_GAP);
  expect(pos.left).toBe(20);
  expect(EDGE_MARGIN).toBe(8);
});
