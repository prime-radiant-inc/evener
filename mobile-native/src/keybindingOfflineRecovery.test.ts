import { describe, expect, it } from "vitest";
import {
	offlineAwareDraftUnreadable,
	offlineStorageErrorMessage,
	unreadableDraftDiscardDisabled,
} from "./keybindingOfflineRecovery";

// These helpers remain pure so the decisions can be tested without importing
// React Native; the screen's JSX wiring has focused coverage in
// KeybindingPreferencesScreen.render.test.tsx.

describe("offlineAwareDraftUnreadable", () => {
	it("prefers the offline probe over a stale domain snapshot while disconnected", () => {
		// domain is guaranteed stale once disconnected: no live read has
		// happened since the connection dropped, so a record that becomes
		// corrupt (or is fixed) while offline is invisible to it.
		expect(offlineAwareDraftUnreadable(false, false, true)).toBe(true);
		expect(offlineAwareDraftUnreadable(false, true, false)).toBe(false);
	});

	it("uses the live domain while connected", () => {
		expect(offlineAwareDraftUnreadable(true, true, false)).toBe(true);
		expect(offlineAwareDraftUnreadable(true, false, true)).toBe(false);
	});

	it("falls back to the offline probe while connected but before any domain exists", () => {
		expect(offlineAwareDraftUnreadable(true, undefined, true)).toBe(true);
		expect(offlineAwareDraftUnreadable(true, undefined, false)).toBe(false);
	});
});

describe("unreadableDraftDiscardDisabled", () => {
	it("is never disabled by a stale hub write flag while offline or without a model", () => {
		// The store-free offline discard touches no hub and has no write of
		// its own to wait on - gating it on domain.writeUncertain left over
		// from before the connection dropped would make recovery impossible.
		expect(unreadableDraftDiscardDisabled(false, { writeUncertain: true })).toBe(false);
		expect(unreadableDraftDiscardDisabled(false, { saving: true, loading: true })).toBe(false);
		expect(unreadableDraftDiscardDisabled(false, undefined)).toBe(false);
	});

	it("is disabled by an in-flight or unresolved hub write while connected with a live model", () => {
		expect(unreadableDraftDiscardDisabled(true, { writeUncertain: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, { saving: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, { loading: true })).toBe(true);
		expect(unreadableDraftDiscardDisabled(true, undefined)).toBe(false);
		expect(unreadableDraftDiscardDisabled(true, {})).toBe(false);
	});
});

describe("offlineStorageErrorMessage", () => {
	it("is null when the port has not failed", () => {
		expect(offlineStorageErrorMessage(false)).toBeNull();
	});

	it("names the failure when the port has failed", () => {
		expect(offlineStorageErrorMessage(true)).toBe(
			"Could not read the saved shortcut draft on this phone. Check current shortcuts to retry.",
		);
	});
});
