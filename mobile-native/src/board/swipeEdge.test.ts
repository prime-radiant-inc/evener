import { expect, it } from "vitest";
import { EDGE_ZONE_PT, edgeBandHitSlop, startsInEdgeZone } from "./swipeEdge";

it("gives the screen's left 24 points to the system's back gesture (spec 7.3)", () => {
	expect(EDGE_ZONE_PT).toBe(24);
	expect([0, 12, 23.9, 24, 200].map(startsInEdgeZone)).toEqual([true, true, true, false, false]);
});

it("measures the edge band from the screen, not from an indented row's own left edge", () => {
	expect(edgeBandHitSlop(0)).toEqual({ left: -24 });
	expect(edgeBandHitSlop(16)).toEqual({ left: -8 });
	expect(edgeBandHitSlop(23)).toEqual({ left: -1 });
	// A row already at or past the screen's edge band needs no trim.
	expect(edgeBandHitSlop(24)).toBeUndefined();
	expect(edgeBandHitSlop(48)).toBeUndefined();
});
