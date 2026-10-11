import { expect, it } from "vitest";
import { EDGE_ZONE_PT, startsInEdgeZone } from "./swipeEdge";

it("gives the screen's left 24 points to the system's back gesture (spec 7.3)", () => {
	expect(EDGE_ZONE_PT).toBe(24);
	expect([0, 12, 23.9, 24, 200].map(startsInEdgeZone)).toEqual([true, true, true, false, false]);
});
