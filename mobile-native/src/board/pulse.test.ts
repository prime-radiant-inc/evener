import { expect, it } from "vitest";
import { fleetMinutes, PULSE_BARS, pulseBars, WORKING_WITHOUT_ACTIVITY } from "./pulse";

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

it("sums the fleet's working sessions bar by bar, aligned the same way pulseBars aligns one", () => {
	expect(fleetMinutes([])).toEqual([0, 0, 0, 0, 0, 0, 0]);
	expect(
		fleetMinutes([
			[1, 2, 3, 4, 5, 6, 7],
			[0, 0, 0, 0, 0, 0, 3],
		]),
	).toEqual([1, 2, 3, 4, 5, 6, 10]);
});

it("right-aligns each session's own history before summing, padding a short one on the left", () => {
	// A 2-entry history lands in the newest two bars, same as pulseBars([64])
	// lands its one entry in the last bar.
	expect(fleetMinutes([[5, 5]])).toEqual([0, 0, 0, 0, 0, 5, 5]);
	expect(
		fleetMinutes([
			[5, 5],
			[1, 1, 1, 1, 1, 1, 1],
		]),
	).toEqual([1, 1, 1, 1, 1, 6, 6]);
});

it("keeps only the newest seven minutes of a longer history, like pulseBars does", () => {
	expect(fleetMinutes([[9, 1, 2, 3, 4, 5, 6, 7]])).toEqual([1, 2, 3, 4, 5, 6, 7]);
});
