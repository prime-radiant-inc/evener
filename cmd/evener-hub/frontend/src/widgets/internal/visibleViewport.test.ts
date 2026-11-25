import { expect, test } from "vitest";
import { positionFromVisibleViewport, rectInVisibleViewport, visibleViewportBounds } from "./visibleViewport";

function ownerWindow(fields: {
  innerWidth: number;
  innerHeight: number;
  visualViewport?: { offsetLeft: number; offsetTop: number; width: number; height: number };
}): Window {
  return fields as unknown as Window;
}

test("without a visual viewport the layout viewport is the visible region", () => {
  expect(visibleViewportBounds(ownerWindow({ innerWidth: 800, innerHeight: 600 }))).toEqual({
    left: 0,
    top: 0,
    width: 800,
    height: 600,
  });
});

test("a visual viewport supplies its own offset and size", () => {
  const bounds = visibleViewportBounds(
    ownerWindow({
      innerWidth: 800,
      innerHeight: 600,
      visualViewport: { offsetLeft: 40, offsetTop: 10, width: 320, height: 400 },
    }),
  );
  expect(bounds).toEqual({ left: 40, top: 10, width: 320, height: 400 });
});

test("a rect and its placement translate into the visible viewport and back", () => {
  const bounds = { left: 40, top: 10, width: 320, height: 400 };
  const rect = { left: 100, right: 180, top: 60, bottom: 100 } as DOMRect;
  const local = rectInVisibleViewport(rect, bounds);
  expect([local.left, local.right, local.top, local.bottom]).toEqual([60, 140, 50, 90]);
  expect(positionFromVisibleViewport({ left: 60, top: 50 }, bounds)).toEqual({ left: 100, top: 60 });
});
