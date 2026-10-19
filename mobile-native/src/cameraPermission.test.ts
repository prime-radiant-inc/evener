// Inspect the permissions Expo generates from source, without reading a local
// ios directory that may belong to a different SDK or scene lifecycle.
import { mkdtempSync, readFileSync, rmdirSync, symlinkSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getPrebuildConfigAsync } from "@expo/prebuild-config";
import { compileModsAsync } from "expo/config-plugins";
import { afterEach, expect, it, vi } from "vitest";

afterEach(() => vi.unstubAllEnvs());

it("generates camera permission without microphone access", async () => {
	const root = join(__dirname, "..");
	vi.stubEnv("APPLE_TEAM_ID", "");
	const { exp } = await getPrebuildConfigAsync(root, { platforms: ["ios"] });
	const projectRoot = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "camera-permission-"));
	try {
		// Info.plist introspection still reads local files even when Expo's
		// ignoreExistingNativeFiles flag is set. Fonts need the real install.
		symlinkSync(join(root, "node_modules"), join(projectRoot, "node_modules"), "dir");
		try {
			const config = await compileModsAsync(exp, { projectRoot, platforms: ["ios"], introspect: true });
			expect(config.ios?.infoPlist).toHaveProperty("NSCameraUsageDescription", expect.any(String));
			expect(config.ios?.infoPlist).not.toHaveProperty("NSMicrophoneUsageDescription");
		} finally {
			unlinkSync(join(projectRoot, "node_modules"));
		}
	} finally {
		rmdirSync(projectRoot);
	}
}, 60_000);

it("builds expo-camera's QR scanner from source, alongside the camera it plugs into", () => {
	// The scanner is ExpoCameraBarcodeScanning: without it the camera reports
	// no codes at all (expo-camera's BarcodeScanner returns early with no
	// provider). Its precompiled framework links a dynamic ExpoCamera, but
	// buildFromSource (package.json) builds ExpoCamera static, and dyld stopped
	// the app at launch. Built from source, both link the same way.
	const lock = readFileSync(join(__dirname, "..", "Podfile.lock"), "utf8");
	const [, afterSources] = lock.split("EXTERNAL SOURCES:");
	if (!afterSources) throw new Error("Podfile.lock has no EXTERNAL SOURCES section");
	const pods = afterSources.split("SPEC CHECKSUMS:")[0] ?? "";
	expect(pods).toMatch(/ExpoCamera:\n {4}:path: "..\/node_modules\/expo-camera\/ios"/);
	expect(pods).toMatch(/ExpoCameraBarcodeScanning:\n {4}:path: "..\/node_modules\/expo-camera\/ios"/);
});
