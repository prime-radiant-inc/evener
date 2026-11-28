import { describe, expect, it } from "vitest";
import { floatingShadow } from "./floatingShadow";

describe("floatingShadow", () => {
	it("owns the float family's color, opacity and elevation; the caller owns offset and radius", () => {
		expect(floatingShadow({ width: 2, height: 0 }, 8)).toEqual({
			shadowColor: "#000",
			shadowOpacity: 0.12,
			shadowRadius: 8,
			shadowOffset: { width: 2, height: 0 },
			elevation: 3,
		});
	});

	it("passes each surface's own offset and radius through unchanged", () => {
		expect(floatingShadow({ width: 0, height: 4 }, 12)).toEqual({
			shadowColor: "#000",
			shadowOpacity: 0.12,
			shadowRadius: 12,
			shadowOffset: { width: 0, height: 4 },
			elevation: 3,
		});
	});
});
