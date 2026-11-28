import { expect, it } from "vitest";
import { connectionFailure, INCOMPATIBLE_VERSIONS, isFatalConnectionFailure } from "./connectionRecovery";

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

it("sends a below-floor hub to its host instead of blaming the app or the network", () => {
	expect(connectionFailure("hub-below-floor").kind).toBe("hub-below-floor");
	expect(isFatalConnectionFailure("hub-below-floor")).toBe(true);
	expect(isFatalConnectionFailure("protocol")).toBe(true);
	expect(isFatalConnectionFailure("transport")).toBe(false);
});
