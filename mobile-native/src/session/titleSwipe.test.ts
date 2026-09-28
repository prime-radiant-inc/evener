import { describe, expect, it } from "vitest";
import { titleSwipeDirection } from "./titleSwipe";

describe("which way a pan on the title moves through Live order (spec 6)", () => {
	it.each([
		{ translationX: -80, velocityX: 0, direction: 1 },
		{ translationX: 80, velocityX: 0, direction: -1 },
		{ translationX: -30, velocityX: -900, direction: 1 },
		{ translationX: 30, velocityX: 900, direction: -1 },
		{ translationX: 30, velocityX: 100, direction: null },
		{ translationX: -60, velocityX: -500, direction: null },
		{ translationX: 0, velocityX: 0, direction: null },
		// Dragged one way, then flicked back the other: no move.
		{ translationX: -80, velocityX: 900, direction: null },
	])("$translationX pt at $velocityX pt/s is $direction", ({ translationX, velocityX, direction }) => {
		expect(titleSwipeDirection(translationX, velocityX)).toBe(direction);
	});
});
