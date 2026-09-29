// Runs check-podfile-lock.mjs against the real mobile-native/Podfile.lock and
// copies of it with one DEPENDENCIES line dropped, built in a temp dir.
// Needs the installed mobile-native/node_modules; `make check-podfile-lock`
// runs it (node --test).
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const script = path.join(here, "check-podfile-lock.mjs");
const realLockPath = path.join(here, "../../mobile-native/Podfile.lock");
const realLock = readFileSync(realLockPath, "utf8");
const scratch = mkdtempSync(path.join(tmpdir(), "check-podfile-lock-"));
after(() => rmSync(scratch, { recursive: true, force: true }));

const run = (scriptPath, args = [], env = {}) =>
	spawnSync(process.execPath, [scriptPath, ...args], { encoding: "utf8", env: { ...process.env, ...env } });
const check = (...args) => run(script, args);

/** A throwaway mobile-native whose expo stub prints FAKE_RESOLVE or FAKE_CONFIG
 * as autolinking's output, chosen by the subcommand it runs. */
function fakeScript() {
	const fake = mkdtempSync(path.join(scratch, "fake-"));
	const expoBin = path.join(fake, "mobile-native/node_modules/expo/bin");
	mkdirSync(expoBin, { recursive: true });
	writeFileSync(path.join(fake, "mobile-native/node_modules/expo/package.json"), '{"name":"expo","version":"0.0.0"}');
	writeFileSync(
		path.join(expoBin, "autolinking.js"),
		'process.stdout.write(process.argv.includes("react-native-config") ? process.env.FAKE_CONFIG : process.env.FAKE_RESOLVE);',
	);
	const scriptDir = path.join(fake, "scripts/native");
	mkdirSync(scriptDir, { recursive: true });
	copyFileSync(script, path.join(scriptDir, "check-podfile-lock.mjs"));
	return path.join(scriptDir, "check-podfile-lock.mjs");
}

const fakeRun = (resolve, config = { reactNativePath: "/x", dependencies: {} }) =>
	run(fakeScript(), ["--lock", realLockPath], { FAKE_RESOLVE: JSON.stringify(resolve), FAKE_CONFIG: JSON.stringify(config) });

/** A copy of the real lock without the DEPENDENCIES line for `pod`. */
function lockWithout(pod) {
	const line = new RegExp(`^ {2}- "?${pod} \\(from .*\\n`, "m");
	assert.match(realLock, line, `the real lock has no DEPENDENCIES line for ${pod}`);
	const file = path.join(scratch, `without-${pod}.lock`);
	writeFileSync(file, realLock.replace(line, ""));
	return file;
}

test("the committed lock passes", () => {
	const run = check();
	assert.equal(run.status, 0, run.stderr);
	assert.match(run.stdout, /locks all \d+ autolinked iOS pods/);
});

test("a lock missing react-native-webview, as before #3295, fails naming it", () => {
	const run = check("--lock", lockWithout("react-native-webview"));
	assert.equal(run.status, 1);
	assert.match(run.stderr, /missing: react-native-webview \(\.\.\/node_modules\/react-native-webview\)/);
});

test("a lock missing a companion pod that shares its module's directory fails", () => {
	const run = check("--lock", lockWithout("ExpoCameraBarcodeScanning"));
	assert.equal(run.status, 1);
	assert.match(run.stderr, /missing: ExpoCameraBarcodeScanning \(\.\.\/node_modules\/expo-camera\/ios\)/);
});

test("a lock listing a pod nothing autolinks fails naming it as extra", () => {
	const file = path.join(scratch, "with-extra.lock");
	writeFileSync(file, realLock.replace("\nDEPENDENCIES:\n", "\nDEPENDENCIES:\n  - FakePod (from `../node_modules/react-native-fake`)\n"));
	const run = check("--lock", file);
	assert.equal(run.status, 1);
	assert.match(run.stderr, /extra: FakePod \(\.\.\/node_modules\/react-native-fake\)/);
});

test("a lock that does not exist exits 2 naming it", () => {
	const run = check("--lock", path.join(scratch, "absent.lock"));
	assert.equal(run.status, 2);
	assert.match(run.stderr, /^check-podfile-lock: cannot read .*absent\.lock/);
});

test("--lock with no value exits 2", () => {
	const run = check("--lock");
	assert.equal(run.status, 2);
	assert.match(run.stderr, /argument missing/);
});

test("a file with no DEPENDENCIES section exits 2", () => {
	const file = path.join(scratch, "not-a-lock");
	writeFileSync(file, "PODS:\n  - A\n");
	const run = check("--lock", file);
	assert.equal(run.status, 2);
	assert.match(run.stderr, /no DEPENDENCIES section/);
});

test("a lock with CRLF line endings is read, not called sectionless", () => {
	const file = path.join(scratch, "crlf.lock");
	writeFileSync(file, realLock.replace(/\n/g, "\r\n"));
	const run = check("--lock", file);
	assert.equal(run.status, 0, run.stderr);
});

test("a DEPENDENCIES section at end of file with no trailing blank line is read", () => {
	const start = realLock.indexOf("\nDEPENDENCIES:\n");
	const file = path.join(scratch, "eof.lock");
	writeFileSync(
		file,
		`${realLock.slice(0, start)}\nDEPENDENCIES:\n  - ExpoCamera (from \`../node_modules/expo-camera/ios\`)`,
	);
	const run = check("--lock", file);
	assert.equal(run.status, 1, run.stderr);
	assert.match(run.stderr, /missing: /);
	assert.doesNotMatch(run.stderr, /no DEPENDENCIES section/);
});

test("a DEPENDENCIES line that does not parse exits 2 naming it", () => {
	const file = path.join(scratch, "garbage.lock");
	writeFileSync(file, realLock.replace("\nDEPENDENCIES:\n", "\nDEPENDENCIES:\n  - BrokenPod (from `../node_modules/broken)\n"));
	const run = check("--lock", file);
	assert.equal(run.status, 2, run.stderr);
	assert.match(run.stderr, /cannot parse/);
});

test("a spec-repo dependency line with no local path is skipped", () => {
	const file = path.join(scratch, "spec-repo.lock");
	writeFileSync(file, realLock.replace("\nDEPENDENCIES:\n", "\nDEPENDENCIES:\n  - Firebase/Core\n  - SomePod (~> 1.0)\n"));
	const run = check("--lock", file);
	assert.equal(run.status, 0, run.stderr);
});

test("a remote Git dependency line with options is skipped", () => {
	const file = path.join(scratch, "git-dep.lock");
	writeFileSync(
		file,
		realLock.replace("\nDEPENDENCIES:\n", "\nDEPENDENCIES:\n  - SomePod (from `https://github.com/foo/bar.git`, branch `main`)\n"),
	);
	const run = check("--lock", file);
	assert.equal(run.status, 0, run.stderr);
});

test("an unexpected autolinking shape exits 2 with one line, not a stack trace", () => {
	// A throwaway mobile-native whose expo stub prints the wrong JSON shape, so
	// the check reaches the autolinking parse without a real dependency tree.
	const result = fakeRun({ modules: "not-an-array" });
	assert.equal(result.status, 2, result.stderr);
	assert.match(result.stderr, /^check-podfile-lock: .*modules/);
	assert.doesNotMatch(result.stderr, /TypeError|at Object|at Module/);
});

test("an autolinking module without pods exits 2 with one line", () => {
	const result = fakeRun({ modules: [{ packageName: "expo-camera" }] });
	assert.equal(result.status, 2, result.stderr);
	assert.match(result.stderr, /^check-podfile-lock: .*module without pods/);
	assert.doesNotMatch(result.stderr, /TypeError|at Object|at Module/);
});

test("an autolinking pod without a name and directory exits 2 with one line", () => {
	const result = fakeRun({ modules: [{ packageName: "expo-camera", pods: [{ podName: "ExpoCamera" }] }] });
	assert.equal(result.status, 2, result.stderr);
	assert.match(result.stderr, /^check-podfile-lock: .*pod/);
	assert.doesNotMatch(result.stderr, /TypeError|at Object|at Module/);
});

test("a react-native-config that is not an object exits 2 with one line", () => {
	const result = fakeRun({ modules: [] }, "not-an-object");
	assert.equal(result.status, 2, result.stderr);
	assert.match(result.stderr, /^check-podfile-lock: .*react-native-config/);
	assert.doesNotMatch(result.stderr, /TypeError|at Object|at Module/);
});

test("a react-native-config without reactNativePath exits 2 with one line", () => {
	const result = fakeRun({ modules: [] }, {});
	assert.equal(result.status, 2, result.stderr);
	assert.match(result.stderr, /^check-podfile-lock: .*reactNativePath/);
	assert.doesNotMatch(result.stderr, /TypeError|at Object|at Module/);
});
