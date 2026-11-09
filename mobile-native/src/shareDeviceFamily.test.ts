// The share extension must stay iPhone-only like its host app: the approved
// device-scope contract. The Apple-targets generator defaults extension
// targets to TARGETED_DEVICE_FAMILY "1,2", and its repeat update replaces the
// target's build configuration list, so the app reapplies the setting after
// the generator through the same Xcode-project mod. This generates a real
// project from the installed template and opens it with the real parser.
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { expect, it, onTestFinished } from "vitest";
import shareIPhoneOnly from "../plugins/with-share-iphone-only";
import { requireFrom } from "./nativeTooling.testkit";

it("generates the share extension iPhone-only", async () => {
	const apple = requireFrom(["@bacons/apple-targets"]);
	const root = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "share-family-"));
	onTestFinished(() => rmSync(root, { recursive: true, force: true }));
	const template = join(dirname(requireFrom([]).resolve("expo/package.json")), "template.tgz");
	execFileSync("tar", ["-xzf", template, "-C", root, "--strip-components=1", "package/ios"]);
	const target = join(root, "targets/EvenerShareExtension");
	mkdirSync(target, { recursive: true });
	writeFileSync(
		join(target, "expo-target.config.js"),
		`module.exports = () => ({
	type: "share",
	name: "EvenerShareExtension",
	bundleIdentifier: ".ShareExtension",
	deploymentTarget: "16.4",
	frameworks: ["UniformTypeIdentifiers"],
	entitlements: {
		"com.apple.security.application-groups": ["group.com.primeradiant.evener.native"],
	},
});
`,
	);
	let config = shareIPhoneOnly({
		name: "HelloWorld",
		slug: "hello-world",
		version: "1.0.0",
		ios: {
			bundleIdentifier: "com.primeradiant.evener.native",
			appleTeamId: "EVENERCI01",
		},
		_internal: { projectRoot: root },
	});
	// Same registration order as app.config.js: the mod before the generator,
	// because same-name mods run in reverse registration order.
	config = apple("./config-plugin")(config);
	const compiled = await apple("expo/config-plugins").compileModsAsync(config, {
		projectRoot: root,
		platforms: ["ios"],
	});
	expect(compiled.extra.eas.build.experimental.ios.appExtensions).toEqual([
		{
			targetName: "EvenerShareExtension",
			bundleIdentifier: "com.primeradiant.evener.native.ShareExtension",
			entitlements: { "com.apple.security.application-groups": ["group.com.primeradiant.evener.native"] },
		},
	]);
	const project = apple("@bacons/xcode").XcodeProject.open(join(root, "ios/HelloWorld.xcodeproj/project.pbxproj"));
	type ParsedTarget = { isa: string; props: { name: string; buildConfigurationList: ParsedConfigurationList } };
	type ParsedConfigurationList = { props: { buildConfigurations: ParsedConfiguration[] } };
	type ParsedConfiguration = { props: { buildSettings: Record<string, unknown> } };
	const targets = project.rootObject.props.targets as ParsedTarget[];
	const share = targets.find((entry) => entry.isa === "PBXNativeTarget" && entry.props.name === "EvenerShareExtension");
	expect(share).toBeDefined();
	const families = share?.props.buildConfigurationList.props.buildConfigurations.map(
		(configuration) => configuration.props.buildSettings.TARGETED_DEVICE_FAMILY,
	);
	expect(families?.length).toBeGreaterThan(0);
	// The plist writer emits a bare 1 unquoted, so the parser reads the value
	// back as a number; both forms mean iPhone-only. The runner reads it
	// through xcodebuild, which reports the string.
	for (const family of families ?? []) expect(String(family)).toBe("1");
});
