// iOS keeps one camera sentence per app, and two config plugins write it:
// expo-image-picker (attaching photos) and expo-camera (the pairing scanner).
// Whichever plugin runs last wins, so both carry the same sentence, and this
// reads the Info.plist Expo would generate to prove the one that ships.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";

const CAMERA =
	"Scan the pairing code shown in Evener on your computer, or take photos to attach to your conversations.";

it("asks for the camera with one sentence that covers scanning and photos, and never for the microphone", () => {
	const root = join(__dirname, "..");
	const config = JSON.parse(
		execFileSync(join(root, "node_modules", ".bin", "expo"), ["config", "--type", "introspect", "--json"], {
			cwd: root,
			encoding: "utf8",
			stdio: ["ignore", "pipe", "ignore"],
		}),
	) as { ios: { infoPlist: Record<string, string | undefined> } };
	expect(config.ios.infoPlist.NSCameraUsageDescription).toBe(CAMERA);
	expect(config.ios.infoPlist.NSMicrophoneUsageDescription).toBeUndefined();
}, 60_000);

it("builds expo-camera's QR scanner from source, alongside the camera it plugs into", () => {
	// The scanner is ExpoCameraBarcodeScanning: without it the camera reports
	// no codes at all (expo-camera's BarcodeScanner returns early with no
	// provider). Its precompiled framework links a dynamic ExpoCamera, but
	// buildFromSource (package.json) builds ExpoCamera static, and dyld stopped
	// the app at launch. Built from source, both link the same way.
	const lock = readFileSync(join(__dirname, "..", "Podfile.lock"), "utf8");
	const pods = lock.slice(lock.indexOf("EXTERNAL SOURCES:"), lock.indexOf("SPEC CHECKSUMS:"));
	expect(pods).toMatch(/ExpoCamera:\n {4}:path: "..\/node_modules\/expo-camera\/ios"/);
	expect(pods).toMatch(/ExpoCameraBarcodeScanning:\n {4}:path: "..\/node_modules\/expo-camera\/ios"/);
});
