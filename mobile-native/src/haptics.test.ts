import { beforeEach, expect, it } from "vitest";
import { alertPreferences } from "./alerts/nativeAlertPreferences";
import { haptic } from "./haptics";
import { playedHaptics } from "./renderNative.testkit";

// expo-haptics and the device store are the setup file's fakes
// (vitestSetup.ts); the preference store is the app's own.
beforeEach(() => {
	playedHaptics.length = 0;
	alertPreferences().set({ haptics: true });
});

it("plays spec 16.6's feedback for each kind", () => {
	for (const kind of ["selection", "light", "success", "warning", "rigid"] as const) haptic(kind);
	expect(playedHaptics).toEqual([
		"selection",
		"impact:light",
		"notification:success",
		"notification:warning",
		"impact:rigid",
	]);
});

it("plays nothing when Hub > In-app alerts turns haptics off", () => {
	alertPreferences().set({ haptics: false });
	haptic("warning");
	expect(playedHaptics).toEqual([]);
});
