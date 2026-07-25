import { expect, test } from "vitest";
import { EDGE_MARGIN } from "../popover/computePosition";
import { computeTooltipPosition, TRIGGER_GAP } from "./computePosition";

const VIEWPORT = { width: 1000, height: 800 };

// A DOMRect-shaped literal is enough: computeTooltipPosition reads left, right,
// top, bottom and width only.
function rect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    x: left,
    y: top,
  } as DOMRect;
}

test("centres the bubble on its trigger when there is room on both sides", () => {
  // Trigger spans 400..440, so its centre is 420; a 200-wide bubble centred
  // there starts at 320.
  const pos = computeTooltipPosition(rect(400, 300, 40, 20), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.left).toBe(320);
});

test("opens above the trigger, one gap clear of it, when the bubble fits there", () => {
  const pos = computeTooltipPosition(rect(400, 300, 40, 20), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.top).toBe(300 - TRIGGER_GAP - 28);
});

// The composer's Send is the rightmost control in the app and carries the
// longest of the three chord tooltips; centred with no shift its bubble ran
// past the pane clipping it. Measured live at 1440px before this: the bubble's
// right edge sat at 1439.27 while the pane ended at 1424.
test("shifts the bubble left of a right-edge trigger instead of overflowing", () => {
  // Centred, a 200-wide bubble on a trigger at 950..990 would start at 870 and
  // end at 1070 - 70px past the viewport.
  const pos = computeTooltipPosition(rect(950, 300, 40, 20), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.left).toBe(VIEWPORT.width - 200 - EDGE_MARGIN);
  expect(pos.left + 200).toBeLessThanOrEqual(VIEWPORT.width - EDGE_MARGIN);
});

test("shifts the bubble right of a left-edge trigger instead of overflowing", () => {
  const pos = computeTooltipPosition(rect(4, 300, 40, 20), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.left).toBe(EDGE_MARGIN);
});

// The rail's search button sits 28px from the top of the window, so its bubble
// wanted to start at y = -8: clipped by the viewport with a one-word label.
test("flips the bubble below a trigger too near the top of the viewport for it", () => {
  const pos = computeTooltipPosition(rect(400, 28, 40, 28), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.top).toBe(28 + 28 + TRIGGER_GAP);
  expect(pos.top).toBeGreaterThanOrEqual(EDGE_MARGIN);
});

test("keeps the bubble above when the trigger is near the bottom, where below has no room", () => {
  const pos = computeTooltipPosition(rect(400, 760, 40, 30), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.top).toBe(760 - TRIGGER_GAP - 28);
});

test("clamps into the viewport when neither above nor below has room", () => {
  // A trigger taller than the viewport leaves no gap on either side.
  const pos = computeTooltipPosition(rect(400, 0, 40, 800), { width: 200, height: 28 }, VIEWPORT);
  expect(pos.top).toBe(EDGE_MARGIN);
});

// The one number the two widgets must agree on: a tooltip that reserved a
// different margin than the popover chassis would read as a different system.
test("reserves the same viewport-edge margin the popover chassis does", () => {
  const pos = computeTooltipPosition(rect(950, 300, 40, 20), { width: 200, height: 28 }, VIEWPORT);
  expect(VIEWPORT.width - (pos.left + 200)).toBe(EDGE_MARGIN);
});
