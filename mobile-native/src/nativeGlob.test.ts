import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, onTestFinished } from "vitest";
import { requireFrom } from "./nativeTooling.testkit";

const globOwners = [
	["Apple targets", ["@bacons/apple-targets"]],
	["Apple config", ["@bacons/apple-targets", "@expo/config"]],
	["Apple config plugins", ["@bacons/apple-targets", "@expo/config-plugins"]],
	["Expo CLI", ["expo", "@expo/cli"]],
	["fingerprint", ["expo", "@expo/fingerprint"]],
] as const;
const expectedFiles = [
	".hidden/secret.ts",
	"a/deeper/second.ts",
	"a/first.ts",
	"b/third.js",
	"root.ts",
	"skip/ignored.ts",
];

function fixture(): string {
	// tmpdir() is under /var on macOS, which is a symlink to /private/var. The
	// Apple-target plugin resolves the paths it registers while the fixture
	// root otherwise stays unresolved, so path.relative climbs to the
	// filesystem root instead of producing ../targets/... (#6049). Realpath
	// the root so both sides of that computation agree on every host.
	const root = realpathSync(mkdtempSync(join(tmpdir(), "evener-glob-")));
	onTestFinished(() => rmSync(root, { recursive: true, force: true }));
	mkdirSync(join(root, "folder.ts"));
	for (const file of [...expectedFiles, "root.txt"]) {
		mkdirSync(dirname(join(root, file)), { recursive: true });
		writeFileSync(join(root, file), file);
	}
	return root;
}

function probe(mode: string, chain: readonly string[], root: string, timing = "") {
	const child = spawnSync(
		process.execPath,
		[
			"--max-old-space-size=128",
			"--import",
			requireFrom([]).resolve("tsx"),
			fileURLToPath(new URL("./nativeGlobProbe.mts", import.meta.url)),
			mode,
			JSON.stringify(chain),
			root,
			timing,
		],
		{ cwd: root, encoding: "utf8", timeout: 15_000, killSignal: "SIGKILL" },
	);
	expect(child.error, child.stderr).toBeUndefined();
	expect(child.signal, child.stderr).toBeNull();
	expect(child.status, child.stderr).toBe(0);
	expect(child.stderr).toBe("");
	return JSON.parse(child.stdout);
}

for (const [name, chain] of globOwners) {
	describe(name, () => {
		for (const mode of ["cancel-promise", "cancel-sync", "cancel-stream"]) {
			for (const timing of ["before", "during"]) {
				it(`${mode} stops traversal after ${timing} abort`, () => {
					const root = fixture();
					const cancelled = probe(mode, chain, root, timing);
					expect(cancelled.completed).toBe(true);
					expect(cancelled.aborted).toBe(true);
					expect(cancelled.abortObserved).toBe(true);
					expect.soft(cancelled.directoryReads, JSON.stringify(cancelled)).toBe(timing === "before" ? 0 : 1);
					if (mode === "cancel-stream" && timing === "before") {
						expect(cancelled.readsAfterAbort, JSON.stringify(cancelled)).toBeLessThanOrEqual(1);
						expect(cancelled.operationsAfterAbort).toHaveLength(cancelled.readsAfterAbort);
						for (const operation of cancelled.operationsAfterAbort)
							expect(operation).toEqual({ operation: "lstat", path: root });
						expect(cancelled.files).toEqual([]);
					} else expect(cancelled.readsAfterAbort, JSON.stringify(cancelled)).toBe(0);
				});
			}
		}
		it("stops queued traversal after a paused stream is aborted", () => {
			const cancelled = probe("cancel-stream", chain, fixture(), "paused");
			expect(cancelled.completed).toBe(true);
			expect(cancelled.paused).toBe(true);
			expect(cancelled.aborted).toBe(true);
			expect(cancelled.abortObserved).toBe(true);
			expect(cancelled.readsAfterAbort).toBe(0);
		});
		it("resumes an uncancelled stream to its complete independent file set", () => {
			const resumed = probe("resume", chain, fixture());
			expect(resumed.completed).toBe(true);
			expect(resumed.paused).toBe(true);
			expect(resumed.aborted).toBe(false);
			expect(resumed.files.sort()).toEqual(expectedFiles);
		});
		it("bounds compiled pattern growth in the glob owner's actual minimatch", () => {
			const growth = probe("minimatch-growth", [...chain, "glob"], fixture());
			expect(growth.completed).toBe(true);
			expect(growth.compiledAlternatives).toBeGreaterThan(0);
			expect(growth.compiledAlternatives).toBeLessThanOrEqual(64);
		});
		it("preserves braces, extglobs, ignore, dotfiles, absolute cwd and symlink walks", () => {
			const root = fixture();
			const glob = requireFrom(chain)("glob");
			expect(glob.globSync("**/*.{ts,js}", { cwd: root, nodir: true, ignore: ["skip/**"] }).sort()).toEqual([
				"a/deeper/second.ts",
				"a/first.ts",
				"b/third.js",
				"root.ts",
			]);
			expect(glob.globSync("@(a|b)/**/*.@(ts|js)", { cwd: root, nodir: true }).sort()).toEqual([
				"a/deeper/second.ts",
				"a/first.ts",
				"b/third.js",
			]);
			expect(glob.globSync("**/*.{ts,js}", { cwd: root, dot: true, nodir: true, absolute: true }).sort()).toEqual(
				expectedFiles.map((file) => join(root, file)).sort(),
			);
			symlinkSync(join(root, "a"), join(root, "alias"), "dir");
			expect(glob.globSync("alias/**/*.ts", { cwd: root, follow: true, nodir: true }).sort()).toEqual([
				"alias/deeper/second.ts",
				"alias/first.ts",
			]);
			expect(glob.globSync("folder.ts", { cwd: root, nodir: true })).toEqual([]);
			expect(glob.globSync("folder.ts", { cwd: root, nodir: false })).toEqual(["folder.ts"]);
			symlinkSync(join(root, "a/first.ts"), join(root, "linked.ts"));
			expect(glob.globSync("linked.ts", { cwd: root, nodir: true })).toEqual(["linked.ts"]);
		});
	});
}

it("bounds fingerprint's directly owned minimatch growth", () => {
	const growth = probe("minimatch-growth", ["expo", "@expo/fingerprint"], fixture());
	expect(growth.completed).toBe(true);
	expect(growth.compiledAlternatives).toBeGreaterThan(0);
	expect(growth.compiledAlternatives).toBeLessThanOrEqual(64);
});

it("the actual Expo CLI retains empty, early-match and complete walks", async () => {
	const root = fixture();
	const { anyMatchAsync, everyMatchAsync } = requireFrom(["expo", "@expo/cli"])("./utils/glob");
	expect(await anyMatchAsync("missing/**/*.ts", { cwd: root })).toEqual([]);
	const early: string[] = await anyMatchAsync("**/*.{ts,js}", { cwd: root, nodir: true, dot: true });
	expect(early).toHaveLength(1);
	expect(expectedFiles).toContain(early[0]);
	expect((await everyMatchAsync("**/*.{ts,js}", { cwd: root, nodir: true, dot: true })).sort()).toEqual(expectedFiles);
});

it("the actual fingerprint preserves negation, dotfile and monorepo-parent ignore semantics", () => {
	const { isIgnoredPath } = requireFrom(["expo", "@expo/fingerprint"])("./utils/Path");
	const ignores = ["**/node_modules/**", "!**/node_modules/keep/**", "**/.cache/**", "**/*.generated.ts"];
	for (const [path, expected] of [
		["src/main.ts", false],
		["node_modules/pkg/index.js", true],
		["node_modules/keep/index.js", false],
		[".cache/index", true],
		["../../packages/pkg/node_modules/other/index.js", true],
		["../../packages/pkg/node_modules/keep/index.js", false],
		["../shared/value.generated.ts", true],
	] as const)
		expect(isIgnoredPath(path, ignores)).toBe(expected);
});

for (const format of ["json", "js"]) {
	it(`the Apple-target plugin discovers ${format} targets and registers their real files`, () => {
		const root = fixture();
		const template = join(requireFrom([]).resolve("expo/package.json"), "..", "template.tgz");
		execFileSync("tar", ["-xzf", template, "-C", root, "--strip-components=1", "package/ios"]);
		const config = { type: "share", name: "FixtureShare", bundleIdentifier: ".FixtureShare" };
		const files = {
			[`targets/FixtureShare/expo-target.config.${format}`]:
				format === "json"
					? JSON.stringify(config)
					: `module.exports = config => ({ ...${JSON.stringify(config)}, bundleIdentifier: config.ios.bundleIdentifier + '.FixtureShare' });`,
			"targets/FixtureShare/Fixture.entitlements": requireFrom(["@bacons/apple-targets"])("@expo/plist").default.build({
				"com.apple.security.application-groups": ["group.com.primeradiant.evener.native"],
			}),
			"targets/FixtureShare/preview/Sample.xcassets/Contents.json": "{}",
			"targets/FixtureShare/_shared/local.swift": "import Foundation",
			"targets/FixtureShare/_shared/.hidden.swift": "import Foundation",
			"targets/_shared/global.swift": "import Foundation",
			"targets/_shared/.hidden.swift": "import Foundation",
			"targets/.hidden/expo-target.config.json": JSON.stringify({ type: "widget", name: "MustRemainHidden" }),
			"targets/nested/too/deep/expo-target.config.json": JSON.stringify({ type: "widget", name: "MustRemainNested" }),
		};
		for (const [file, content] of Object.entries(files)) {
			mkdirSync(dirname(join(root, file)), { recursive: true });
			writeFileSync(join(root, file), content);
		}
		const generated = probe("apple-targets", ["@bacons/apple-targets"], root);
		expect(generated.completed).toBe(true);
		expect(generated.apple.extensions).toEqual([
			{
				targetName: "FixtureShare",
				bundleIdentifier: "com.primeradiant.evener.native.FixtureShare",
				entitlements: { "com.apple.security.application-groups": ["group.com.primeradiant.evener.native"] },
			},
		]);
		type PBXObject = {
			isa: string;
			name?: string;
			path?: string;
			target?: string;
			membershipExceptions?: string[];
			buildSettings?: Record<string, unknown>;
		};
		const objects = generated.apple.project.objects as Record<string, PBXObject>;
		const values = Object.values(objects);
		expect(
			values
				.filter((object) => object.isa === "PBXNativeTarget")
				.map((object) => object.name)
				.sort(),
		).toEqual(["FixtureShare", "HelloWorld"]);
		const settings = values.filter(
			(object) => object.buildSettings?.PRODUCT_BUNDLE_IDENTIFIER === "com.primeradiant.evener.native.FixtureShare",
		);
		expect(settings).toHaveLength(2);
		for (const object of settings)
			expect(object.buildSettings).toMatchObject({
				CODE_SIGN_ENTITLEMENTS: "../targets/FixtureShare/Fixture.entitlements",
				REGISTER_APP_GROUPS: "YES",
				DEVELOPMENT_ASSET_PATHS: '"../targets/FixtureShare/preview"',
			});
		const host = Object.entries(objects).find(
			([, object]) => object.isa === "PBXNativeTarget" && object.name === "HelloWorld",
		);
		expect(host).toBeDefined();
		expect(values.filter((object) => object.membershipExceptions?.includes("_shared/local.swift"))).toEqual([
			expect.objectContaining({ target: host?.[0], membershipExceptions: ["_shared/local.swift"] }),
		]);
		const globalExceptions = values.filter((object) => object.membershipExceptions?.includes("_shared/global.swift"));
		expect(globalExceptions).toHaveLength(2);
		for (const object of globalExceptions) expect(object.membershipExceptions).toEqual(["_shared/global.swift"]);
		expect(
			values
				.filter((object) => object.isa === "PBXFileSystemSynchronizedRootGroup")
				.map((object) => object.path)
				.sort(),
		).toEqual(["FixtureShare", "_shared"]);
	});
}
