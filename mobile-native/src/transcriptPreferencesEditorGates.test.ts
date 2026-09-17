import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { NativePreferencesSnapshot } from "./nativePreferences";
import { transcriptEditingDisabled } from "./transcriptPreferencesEditorGates";

const config = makeTranscriptDisplayConfig();

function baseState(): NativePreferencesSnapshot["transcriptMobile"] {
	return {
		support: "supported",
		loading: false,
		saving: false,
		confirmed: null,
		draft: null,
		error: null,
		conflict: false,
		writeUncertain: false,
		storageUnavailable: false,
		draftUnreadable: false,
	};
}

// Round 16 Medium 1: a restored draft renders synchronously from the port,
// before the hub's first read ever lands - `confirmed` stays null through
// that whole window even while `connected` is true and nothing else in
// PreferenceState says "disabled". Editing and saving both compose against
// the confirmed value (assertEditable throws until the store's `loaded`),
// so the disabled state for THOSE controls must say so, not just `disabled`.
describe("transcriptEditingDisabled", () => {
	it("disables editing while nothing is confirmed yet, even with a restored draft and a live connection", () => {
		const state = { ...baseState(), draft: { revision: 4, config } };
		expect(transcriptEditingDisabled(state, true)).toBe(true);
	});

	it("enables editing once the hub confirms a value", () => {
		const state = { ...baseState(), confirmed: { revision: 4, config }, draft: { revision: 4, config } };
		expect(transcriptEditingDisabled(state, true)).toBe(false);
	});

	it("stays disabled while disconnected even with a confirmed value", () => {
		const state = { ...baseState(), confirmed: { revision: 4, config } };
		expect(transcriptEditingDisabled(state, false)).toBe(true);
	});

	it.each([
		["loading", { loading: true }],
		["saving", { saving: true }],
		["writeUncertain", { writeUncertain: true }],
		["storageUnavailable", { storageUnavailable: true }],
	] as const)("stays disabled while %s, even with a confirmed value", (_name, patch) => {
		const state = { ...baseState(), confirmed: { revision: 4, config }, ...patch };
		expect(transcriptEditingDisabled(state, true)).toBe(true);
	});
});
