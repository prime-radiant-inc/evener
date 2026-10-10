import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { beforeEach, expect, it } from "vitest";
import { alertPreferences } from "./alerts/nativeAlertPreferences";
import { destructiveButton, haptic } from "./haptics";
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

it("confirms a destructive action with a rigid impact, at the press (spec 16.6)", async () => {
	const done: string[] = [];
	const button = destructiveButton("Delete", () => done.push("deleted"));
	expect(button).toMatchObject({ text: "Delete", style: "destructive" });
	expect(playedHaptics).toEqual([]);
	button.onPress();
	expect(playedHaptics).toEqual(["impact:rigid"]);
	expect(done).toEqual(["deleted"]);
});

// Every destructive confirmation goes through destructiveButton, so none can
// forget its haptic. The one exception opens a confirmation of its own,
// which plays it.
const OPENS_A_CONFIRMATION: Record<string, number> = { "src/board/BoardScreen.tsx": 1 };

it("builds every destructive Alert button with destructiveButton", () => {
	const root = fileURLToPath(new URL("..", import.meta.url));
	const offenders = readdirSync(path.join(root, "src"), { recursive: true, encoding: "utf8" })
		.filter((file) => /\.tsx?$/.test(file) && !/\.(test|testkit)\.tsx?$/.test(file))
		.map((file) => path.join("src", file))
		.filter((file) => file !== "src/haptics.ts")
		.flatMap((file) => {
			const count = readFileSync(path.join(root, file), "utf8").split('style: "destructive"').length - 1;
			return count > (OPENS_A_CONFIRMATION[file] ?? 0) ? [`${file}: ${count}`] : [];
		});
	expect(offenders).toEqual([]);
});
