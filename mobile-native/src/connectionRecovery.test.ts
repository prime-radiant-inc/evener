import { expect, it } from "vitest";
import { connectionFailure, INCOMPATIBLE_VERSIONS } from "./connectionRecovery";

it("says spec 14's sentence for a close no retry can fix", () => {
	expect(connectionFailure("protocol")).toEqual({ kind: "protocol", message: INCOMPATIBLE_VERSIONS });
	expect(INCOMPATIBLE_VERSIONS).toBe(
		"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.",
	);
});

it("says what to check when the hub can't be reached, and never asks you to reconnect", () => {
	expect(connectionFailure(null)).toEqual({
		kind: "transport",
		message: "Couldn't reach the hub. Check its address, its token and your network.",
	});
});
