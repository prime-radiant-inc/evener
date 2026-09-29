#!/usr/bin/env node
// check-podfile-lock.mjs: fail when mobile-native/Podfile.lock does not list
// exactly the iOS pods autolinking resolves from node_modules.
//
// The iOS build installs pods with `pod install --deployment`, which refuses
// a lock that lacks a pod the Podfile now asks for. Only the TestFlight
// workflow runs that, on a tag, so a native dependency added to package.json
// without its pod passed every PR check and broke the archive (#3252, fixed
// by #3295; #3294). This check needs no macOS, CocoaPods or generated ios/
// project: it asks the same two autolinking commands the generated Podfile
// runs (Expo modules, and React Native community modules through Expo's
// react-native-config) which pods they link and from which directory, and
// compares each pod name and directory with the lock's DEPENDENCIES.
//
// Pods React Native itself declares (under the react-native package) and the
// codegen pods (under build/generated/ios) come from use_react_native!, not
// autolinking, so they are left out of the comparison. A version change
// inside a pod that is already locked is not something this can see; the
// deployment install in the TestFlight workflow still catches that.
//
// Not handled: Expo's precompiled mode (EXPO_USE_PRECOMPILED_MODULES=1),
// where pods point at prebuilt podspecs instead of package directories, and
// the extraPods / extraDependencies an app can add through config (this app
// uses neither); and a community module whose podspec file is named apart
// from the pod it declares, or whose podspec leaves out iOS, since the pod's
// name is taken from the file (the Podfile reads the spec itself).

import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const HELP = `Usage: scripts/native/check-podfile-lock.mjs [--lock PATH]

Checks that mobile-native/Podfile.lock lists exactly the iOS pods autolinking
resolves from mobile-native/node_modules, and fails naming each pod the lock
is missing and each autolinked pod it lists that nothing links any more.

When to run it: after adding, removing or moving a native dependency in
mobile-native/package.json; CI runs it in the native job (make
check-podfile-lock). Needs a real (not symlinked) mobile-native/node_modules
from npm ci; no macOS, CocoaPods or ios/ project.

Options:
  --lock PATH  check this lock file instead of mobile-native/Podfile.lock
               (for example an older one from git show)
  -h, --help   show this help

To fix a failure, follow "Regenerating the pod lock" in
docs/design/mobile/ios-build-distribution.md: from mobile-native, prebuild,
copy Podfile.lock into ios/, run pod install without --deployment, copy
ios/Podfile.lock back, and commit only Podfile.lock.`;

// Companion pods: Expo registers these beside a module's main pod, from the
// same directory, when the module's spm.config.json "autolinkWhen" condition
// holds. `expo-modules-autolinking resolve` never lists them (the Podfile's
// Ruby side adds them), so they are named here with the package they ship in
// and the condition they wait on. ExpoCameraBarcodeScanning waits on the
// expo.camera.barcode-scanner-enabled Podfile property, which mobile-native's
// app.json never sets to "false" — set it there through the expo-camera
// plugin's barcodeScannerEnabled option, and update this condition (`when`)
// if anyone ever does; ExpoModulesWorkletsAdapter waits on the RNWorklets pod,
// which react-native-worklets provides. A linked package that declares a
// companion not named here fails the check (declaredCompanions), so a new
// one is added to this list, with its condition, rather than slipping past.
const COMPANIONS = [
	{ pod: "ExpoCameraBarcodeScanning", packageName: "expo-camera", when: () => true },
	{
		pod: "ExpoModulesWorkletsAdapter",
		packageName: "expo-modules-core",
		when: (config) => "react-native-worklets" in (config.dependencies ?? {}),
	},
];

const nativeDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../mobile-native");
// The lock's paths are relative to the generated ios/ directory.
const iosDir = path.join(nativeDir, "ios");

function fail(message) {
	console.error(`check-podfile-lock: ${message}`);
	process.exit(2);
}

function options() {
	try {
		const { values } = parseArgs({
			options: { lock: { type: "string" }, help: { type: "boolean", short: "h" } },
		});
		if (values.help) {
			console.log(HELP);
			process.exit(0);
		}
		return { lock: path.resolve(values.lock ?? path.join(nativeDir, "Podfile.lock")) };
	} catch (error) {
		fail(`${error.message}\n\n${HELP}`);
	}
}

/** Runs one of Expo's autolinking commands the Podfile runs, as JSON. */
function autolinking(...args) {
	try {
		const out = execFileSync(
			process.execPath,
			["--no-warnings", "--eval", "require('expo/bin/autolinking')", "expo-modules-autolinking", ...args, "--json"],
			{ cwd: nativeDir, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], maxBuffer: 64 * 1024 * 1024 },
		);
		return JSON.parse(out);
	} catch (error) {
		fail(`expo-modules-autolinking ${args.join(" ")} failed: ${error.message}`);
	}
}

/** A directory as the lock writes it: relative to ios/, POSIX, no trailing slash. */
function lockPath(dir) {
	return path.posix.normalize(path.relative(iosDir, dir).split(path.sep).join("/")).replace(/\/$/, "");
}

const podKey = (pod, dir) => `${pod} (${dir})`;

/** The pods autolinking links, as "Pod (dir)", each with the package it came from. */
function resolvedPods() {
	const linked = new Map();
	const packageDirs = new Map();
	const resolved = autolinking("resolve", "--platform", "apple");
	if (!Array.isArray(resolved?.modules)) {
		fail(`expo-modules-autolinking resolve did not return a modules list: ${JSON.stringify(resolved)}`);
	}
	const expoModules = resolved.modules;
	for (const module of expoModules) {
		if (!Array.isArray(module?.pods)) {
			fail(`expo-modules-autolinking resolve returned a module without pods: ${JSON.stringify(module)}`);
		}
		for (const pod of module.pods) {
			if (typeof pod?.podName !== "string" || typeof pod?.podspecDir !== "string") {
				fail(`expo-modules-autolinking resolve returned a pod without a name and directory: ${JSON.stringify(pod)}`);
			}
			linked.set(podKey(pod.podName, lockPath(pod.podspecDir)), module.packageName);
			if (!packageDirs.has(module.packageName)) packageDirs.set(module.packageName, lockPath(pod.podspecDir));
		}
	}
	const config = autolinking("react-native-config", "--platform", "ios");
	if (config === null || typeof config !== "object") {
		fail(`expo-modules-autolinking react-native-config did not return an object: ${JSON.stringify(config)}`);
	}
	if (typeof config.reactNativePath !== "string") {
		fail(`expo-modules-autolinking react-native-config returned no reactNativePath: ${JSON.stringify(config.reactNativePath)}`);
	}
	for (const [name, dependency] of Object.entries(config.dependencies ?? {})) {
		const podspec = dependency?.platforms?.ios?.podspecPath;
		// A community module's pod is named for its podspec file, which is how
		// CocoaPods finds it in the directory the Podfile points at.
		if (podspec) linked.set(podKey(path.basename(podspec, ".podspec"), lockPath(path.dirname(podspec))), name);
	}
	for (const companion of COMPANIONS) {
		const dir = packageDirs.get(companion.packageName);
		if (dir && companion.when(config)) linked.set(podKey(companion.pod, dir), companion.packageName);
	}
	const unknown = declaredCompanions(expoModules, Object.keys(config.dependencies ?? {})).filter(
		(declared) => !COMPANIONS.some((companion) => companion.pod === declared.pod),
	);
	return { linked, unknown, reactNative: lockPath(config.reactNativePath) };
}

/** The companion pods the linked packages declare: spm.config.json products
 * with autolinkWhen, found where Expo's Ruby side looks for them (beside an
 * Expo module's first podspec directory or one above it, and in
 * expo-modules-autolinking's external-configs for community packages). */
function autolinkingPackageDir() {
	try {
		return path.dirname(
			createRequire(path.join(nativeDir, "node_modules/expo/package.json")).resolve("expo-modules-autolinking/package.json"),
		);
	} catch (error) {
		fail(`cannot resolve expo-modules-autolinking from mobile-native/node_modules: ${error.message}`);
	}
}

function declaredCompanions(expoModules, communityNames) {
	const configs = [];
	for (const module of expoModules) {
		const dir = module.pods[0]?.podspecDir;
		const found = dir && [dir, path.dirname(dir)].map((d) => path.join(d, "spm.config.json")).find(existsSync);
		if (found) configs.push([module.packageName, found]);
	}
	const autolinkingDir = autolinkingPackageDir();
	for (const name of communityNames) {
		const file = path.join(autolinkingDir, "external-configs/ios", name, "spm.config.json");
		if (existsSync(file)) configs.push([name, file]);
	}
	return configs.flatMap(([packageName, file]) => {
		let parsed;
		try {
			parsed = JSON.parse(readFileSync(file, "utf8"));
		} catch (error) {
			fail(`cannot parse ${file}: ${error.message}`);
		}
		return (parsed?.products ?? [])
			.filter((product) => product.autolinkWhen)
			.map((product) => ({ pod: product.podName ?? product.name, packageName }));
	});
}

/** The lock's DEPENDENCIES that name a local path, as "Pod (dir)". */
function lockedPods(lock) {
	let text;
	try {
		text = readFileSync(lock, "utf8");
	} catch (error) {
		fail(`cannot read ${lock}: ${error.message}`);
	}
	// A lock written on Windows or by a tool that emits CRLF still parses.
	const lines = text.split(/\r?\n/);
	const start = lines.indexOf("DEPENDENCIES:");
	if (start < 0) fail(`${lock} has no DEPENDENCIES section; is it a Podfile.lock?`);
	// The section ends at the next top-level header or at end of file; a lock
	// whose DEPENDENCIES is last has no trailing blank line to stop at.
	const end = lines.findIndex((line, i) => i > start && line !== "" && !line.startsWith(" "));
	const locked = new Map();
	for (const line of lines.slice(start + 1, end < 0 ? undefined : end)) {
		if (line === "") continue;
		const match = /^ {2}- "?([^ "]+) \(from `([^`]+)`\)"?$/.exec(line);
		if (!match) {
			// A spec-repo dependency (`  - Firebase/Core`, `  - SomePod (~> 1.0)`)
			// has no local path and is out of scope; a local-path line this cannot
			// read may be a pod the check would otherwise call missing, so fail it.
			if (line.includes("(from ")) fail(`${lock}'s DEPENDENCIES has a line this check cannot parse: ${JSON.stringify(line)}`);
			continue;
		}
		const dir = path.posix.normalize(match[2]).replace(/\/$/, "");
		locked.set(podKey(match[1], dir), dir);
	}
	return locked;
}

const { lock } = options();
// Through a symlinked install, autolinking reports the link target's real
// paths, which never match the lock's ../node_modules paths.
if (lstatSync(path.join(nativeDir, "node_modules"), { throwIfNoEntry: false })?.isSymbolicLink()) {
	fail("mobile-native/node_modules is a symlink; remove the link and give this checkout its own install there.");
}
const locked = lockedPods(lock);
const { linked, unknown, reactNative } = resolvedPods();
const fromReactNative = (dir) =>
	[reactNative, "build/generated/ios"].some((root) => dir === root || dir.startsWith(`${root}/`));

const missing = [...linked].filter(([key]) => !locked.has(key));
const extra = [...locked].filter(([key, dir]) => !linked.has(key) && !fromReactNative(dir));
const shown = path.relative(process.cwd(), lock).startsWith("..") ? lock : path.relative(process.cwd(), lock);
if (missing.length === 0 && extra.length === 0 && unknown.length === 0) {
	console.log(`check-podfile-lock: ${shown} locks all ${linked.size} autolinked iOS pods.`);
	process.exit(0);
}
console.error(`check-podfile-lock: ${shown} does not match what autolinking resolves.`);
for (const [key, name] of missing) console.error(`  missing: ${key}, from ${name}, is autolinked but not locked`);
for (const [key] of extra) console.error(`  extra: ${key} is locked but nothing autolinks it`);
for (const { pod, packageName } of unknown)
	console.error(
		`  unknown companion: ${pod}, which ${packageName} registers through autolinkWhen; add it to COMPANIONS in scripts/native/check-podfile-lock.mjs with its condition`,
	);
// An unknown companion explains its own extra line; regenerating would not.
if (unknown.length === 0)
	console.error(
		'To regenerate the lock, follow "Regenerating the pod lock" in docs/design/mobile/ios-build-distribution.md: from mobile-native, prebuild, copy Podfile.lock into ios/, run pod install without --deployment, copy ios/Podfile.lock back, and commit only Podfile.lock.',
	);
process.exit(1);
