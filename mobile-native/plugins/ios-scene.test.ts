import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const require = createRequire(import.meta.url);
const { getConfig } = require("@expo/config");
const projectRoot = fileURLToPath(new URL("..", import.meta.url));

async function configuredAppDelegate(contents: string, language = "swift") {
	const { exp } = getConfig(projectRoot, { isModdedConfig: true });
	const input = {
		...exp,
		modResults: { contents, language },
		modRequest: { projectRoot, platformProjectRoot: `${projectRoot}/ios`, platform: "ios", modName: "appDelegate" },
	};
	return exp.mods.ios.appDelegate ? (await exp.mods.ios.appDelegate(input)).modResults.contents : contents;
}

async function configuredInfoPlist(input: Record<string, unknown>) {
	const { exp } = getConfig(projectRoot, { isModdedConfig: true });
	return (
		await exp.mods.ios.infoPlist({
			...exp,
			modResults: structuredClone(input),
			modRequest: { projectRoot, platformProjectRoot: `${projectRoot}/ios`, platform: "ios", modName: "infoPlist" },
		})
	).modResults;
}

describe("iOS scene prebuild", () => {
	it("changes the stock startup once, with identical output on later prebuilds", async () => {
		const stock = readFileSync(new URL("./fixtures/expo57-app-delegate.swift", import.meta.url), "utf8");
		const once = await configuredAppDelegate(stock);
		expect(once).not.toBe(stock);
		expect(await configuredAppDelegate(once)).toBe(once);
	});

	it("replaces stale owned sections while preserving another plugin's content", async () => {
		const stock = readFileSync(new URL("./fixtures/expo57-app-delegate.swift", import.meta.url), "utf8");
		const generated = await configuredAppDelegate(stock);
		const stale = generated.replace(
			/(\/\/ @generated begin evener-[^\n]+\n)[\s\S]*?(\/\/ @generated end evener-[^\n]+)/g,
			"$1obsolete_owned_section\n$2",
		);
		const updated = await configuredAppDelegate(`${stale}\n// external_plugin_sentinel\n`);
		expect(updated).not.toContain("obsolete_owned_section");
		expect(updated.endsWith("// external_plugin_sentinel\n")).toBe(true);
	});

	it.each(["scene-state", "scene-startup", "scene-url", "scene-adapter"])(
		"rejects an incomplete %s section instead of appending a second implementation",
		async (section) => {
			const stock = readFileSync(new URL("./fixtures/expo57-app-delegate.swift", import.meta.url), "utf8");
			const generated = await configuredAppDelegate(stock);
			const incomplete = generated.replace(`// @generated end evener-${section}`, "");
			await expect(configuredAppDelegate(incomplete)).rejects.toThrow();
		},
	);

	it("rejects an unfamiliar startup instead of producing a partial scene migration", async () => {
		await expect(configuredAppDelegate("import UIKit\n@main class AppDelegate: UIResponder {}\n")).rejects.toThrow();
	});

	it("rejects a non-Swift template instead of injecting Swift into it", async () => {
		await expect(configuredAppDelegate("@implementation AppDelegate\n@end\n", "objc")).rejects.toThrow();
	});

	it("registers one application scene without replacing identity, linking or unrelated plist settings", async () => {
		// Missing the scene registration prevents SDK 27 apps from launching.
		const existing = {
			CFBundleIdentifier: "com.primeradiant.evener.native",
			CFBundleURLTypes: [{ CFBundleURLSchemes: ["evener-native"] }],
			NSAppTransportSecurity: { NSAllowsArbitraryLoads: true },
		};
		const plist = await configuredInfoPlist(existing);
		expect(plist).toMatchObject(existing);
		expect(plist.UIApplicationSceneManifest).toEqual({
			UIApplicationSupportsMultipleScenes: false,
			UISceneConfigurations: {
				UIWindowSceneSessionRoleApplication: [
					{
						UISceneConfigurationName: "Evener",
						UISceneDelegateClassName: "$(PRODUCT_MODULE_NAME).SceneDelegate",
					},
				],
			},
		});
	});

	it("keeps the same scene registration on repeated prebuilds", async () => {
		const once = await configuredInfoPlist({});
		const twice = await configuredInfoPlist(once);
		expect(twice).toEqual(once);
	});
});
