import { expect, it } from "vitest";
import { PULSE_BARS, pulseBars, WORKING_WITHOUT_ACTIVITY } from "./pulse";

it("uses one fixed log scale, so a trickle never looks like a flood", () => {
	const bars = pulseBars([0, 1, 8, 64, 500, -3, 0]);
	expect(bars.map((bar) => Number(bar.height.toFixed(3)))).toEqual([0, 0.166, 0.526, 1, 1, 0, 0]);
});

it("fades older minutes so time reads left to right", () => {
	const opacities = pulseBars([1, 1, 1, 1, 1, 1, 1]).map((bar) => bar.opacity);
	expect(opacities[0]).toBeCloseTo(0.35);
	expect(opacities[PULSE_BARS - 1]).toBeCloseTo(1);
	expect([...opacities].sort((a, b) => a - b)).toEqual(opacities);
});

it("pads a short history on the left and keeps only the newest seven minutes", () => {
	expect(pulseBars([64]).map((bar) => bar.height)).toEqual([0, 0, 0, 0, 0, 0, 1]);
	expect(pulseBars([64, 0, 0, 0, 0, 0, 0, 0]).map((bar) => bar.height)).toEqual([0, 0, 0, 0, 0, 0, 0]);
});

it("draws the one-bar fallback until the hub reports activity (S5)", () => {
	expect(pulseBars()).toEqual(pulseBars(WORKING_WITHOUT_ACTIVITY));
	expect(pulseBars().filter((bar) => bar.height > 0)).toHaveLength(1);
});
