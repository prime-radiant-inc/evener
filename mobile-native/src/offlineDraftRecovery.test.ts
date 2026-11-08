import { describe, expect, it } from "vitest";
import { unreadableDraftDiscardDisabled } from "./offlineDraftRecovery";

describe("unreadableDraftDiscardDisabled", () => {
	it("leaves the phone-only discard enabled without a connected model", () => {
		expect(unreadableDraftDiscardDisabled(false, { writeUncertain: true })).toBe(false);
		expect(unreadableDraftDiscardDisabled(false, { saving: true, loading: true })).toBe(false);
		expect(unreadableDraftDiscardDisabled(false, undefined)).toBe(false);
	});

	it("waits on the live store's in-flight work while a model is connected", () => {
		expect(unreadableDraftDiscardDisabled(true, { writeUncertain: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, { saving: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, { loading: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, undefined)).toBe(false);
		expect(unreadableDraftDiscardDisabled(true, {})).toBe(false);
	});
});
