#!/usr/bin/env node
// check-podfile-lock.mjs: fail when mobile-native/Podfile.lock does not list
// exactly the iOS native modules autolinking resolves from node_modules.
//
// The iOS build installs pods with `pod install --deployment`, which refuses
// a lock that lacks a pod the Podfile now asks for. Only the TestFlight
// workflow runs that, on a tag, so a native dependency added to package.json
// without its pod passed every PR check and broke the archive (#3252, fixed
// by #3295; #3294). This check needs no macOS, CocoaPods or generated ios/
// project: it asks the same two autolinking commands the generated Podfile
// runs (Expo modules, and React Native community modules through Expo's
// react-native-config) which module directories they link, and compares
// those with the `(from ...)` paths in the lock's DEPENDENCIES.
//
// Pods React Native itself declares (under the react-native package) and the
// codegen pods (under build/generated/ios) come from use_react_native!, not
// autolinking, so they are left out of the comparison. A version change
// inside a pod that is already locked is not something this can see; the
// deployment install in the TestFlight workflow still catches that.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HELP = `Usage: scripts/native/check-podfile-lock.mjs [--lock PATH]

Checks that mobile-native/Podfile.lock lists exactly the iOS native modules
autolinking resolves from mobile-native/node_modules, and fails naming each
module the lock is missing and each autolinked pod it lists that nothing
links any more.

When to run it: after adding, removing or moving a native dependency in
mobile-native/package.json; CI runs it in the native job (make
check-podfile-lock). Needs an installed mobile-native/node_modules (npm ci);
no macOS, CocoaPods or ios/ project.

Options:
  --lock PATH  check this lock file instead of mobile-native/Podfile.lock
               (for example an older one from git show)
  -h, --help   show this help

To fix a failure, regenerate the lock with the locked dependency procedure
in docs/design/mobile/ios-build-distribution.md (pod install without
--deployment), and commit only the Podfile.lock change.`;

const nativeDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../mobile-native");
// The lock's paths are relative to the generated ios/ directory.
const iosDir = path.join(nativeDir, "ios");

function parseArgs(argv) {
	let lock = path.join(nativeDir, "Podfile.lock");
	for (let i = 0; i < argv.length; i++) {
		const arg = argv[i];
		if (arg === "-h" || arg === "--help") {
			console.log(HELP);
			process.exit(0);
		} else if (arg === "--lock" && i + 1 < argv.length) {
			lock = path.resolve(argv[++i]);
		} else {
			console.error(`check-podfile-lock: unknown argument ${arg}\n\n${HELP}`);
			process.exit(2);
		}
	}
	return { lock };
}

/** Runs one of Expo's autolinking commands the Podfile runs, as JSON. */
function autolinking(...args) {
	const out = execFileSync(
		process.execPath,
		["--no-warnings", "--eval", "require('expo/bin/autolinking')", "expo-modules-autolinking", ...args, "--json"],
		{ cwd: nativeDir, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], maxBuffer: 64 * 1024 * 1024 },
	);
	return JSON.parse(out);
}

/** A directory as the lock writes it: relative to ios/, POSIX, no trailing slash. */
function lockPath(dir) {
	return path.posix.normalize(dir.split(path.sep).join("/")).replace(/\/$/, "");
}

/** The module directories autolinking links, each with the package it came from. */
function resolvedModules() {
	const linked = new Map();
	for (const module of autolinking("resolve", "--platform", "apple").modules) {
		for (const pod of module.pods) {
			linked.set(lockPath(path.relative(iosDir, pod.podspecDir)), module.packageName);
		}
	}
	const config = autolinking("react-native-config", "--platform", "ios");
	for (const [name, dependency] of Object.entries(config.dependencies)) {
		const ios = dependency.platforms?.ios;
		if (ios?.podspecPath) linked.set(lockPath(path.relative(iosDir, path.dirname(ios.podspecPath))), name);
	}
	return { linked, reactNative: lockPath(path.relative(iosDir, config.reactNativePath)) };
}

/** The lock's DEPENDENCIES that name a local path: pod name by directory. */
function lockedModules(lockText) {
	const start = lockText.indexOf("\nDEPENDENCIES:\n");
	const end = lockText.indexOf("\n\n", start + 1);
	if (start < 0 || end < 0) throw new Error("no DEPENDENCIES section in the lock");
	const locked = new Map();
	for (const line of lockText.slice(start, end).split("\n")) {
		const match = /^ {2}- "?([^ "]+) \(from `([^`]+)`\)"?$/.exec(line);
		if (match) locked.set(lockPath(match[2]), match[1]);
	}
	return locked;
}

const { lock } = parseArgs(process.argv.slice(2));
const { linked, reactNative } = resolvedModules();
const locked = lockedModules(readFileSync(lock, "utf8"));
const fromReactNative = (dir) =>
	dir === reactNative || dir.startsWith(`${reactNative}/`) || dir.startsWith("build/generated/ios/");

const missing = [...linked].filter(([dir]) => !locked.has(dir));
const extra = [...locked].filter(([dir]) => !linked.has(dir) && !fromReactNative(dir));
if (missing.length === 0 && extra.length === 0) {
	console.log(`check-podfile-lock: ${path.relative(process.cwd(), lock) || lock} locks all ${linked.size} autolinked iOS modules.`);
	process.exit(0);
}
console.error(`check-podfile-lock: ${lock} does not match what autolinking resolves.`);
for (const [dir, name] of missing) console.error(`  missing: ${name} (${dir}) is autolinked but has no pod in the lock`);
for (const [dir, name] of extra) console.error(`  extra: pod ${name} (${dir}) is locked but nothing autolinks it`);
console.error(
	"Regenerate the lock with the locked dependency procedure in docs/design/mobile/ios-build-distribution.md (pod install without --deployment) and commit only Podfile.lock.",
);
process.exit(1);
