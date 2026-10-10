import { expect, test } from "vitest";
import { installMobileViewport } from "./mobileViewport";

test("mobile viewport matches only the phone breakpoint and restores the original reader", () => {
  const original = window.matchMedia;
  const restore = installMobileViewport();
  try {
    expect(window.matchMedia("(max-width: 899px)").matches).toBe(true);
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    expect(motion.media).toBe("(prefers-reduced-motion: reduce)");
    expect(motion.matches).toBe(false);
  } finally {
    restore();
  }
  expect(window.matchMedia).toBe(original);
});
