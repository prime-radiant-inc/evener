import { expect, test } from "vitest";
import { computePopoverPosition, EDGE_MARGIN, resolveAxis, TRIGGER_GAP } from "./computePosition";

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
