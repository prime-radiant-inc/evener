import { expect, it } from "vitest";
import { swipeDismisses } from "./bannerGesture";

it.each([
	[-31, 0, true],
	[-30, 0, false],
	[-10, -0.6, true],
	[-10, -0.4, false],
	[20, -2, false],
	[0, 0, false],
])("a drag of %ipt at %f pt/ms dismisses: %s", (dy, vy, expected) => {
	expect(swipeDismisses(dy, vy)).toBe(expected);
});
