// Runs check-podfile-lock.mjs against the real mobile-native/Podfile.lock and
// copies of it with one DEPENDENCIES line dropped, built in a temp dir.
// Needs the installed mobile-native/node_modules; `make check-podfile-lock`
// runs it (node --test).
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const script = path.join(here, "check-podfile-lock.mjs");
const realLock = readFileSync(path.join(here, "../../mobile-native/Podfile.lock"), "utf8");
const scratch = mkdtempSync(path.join(tmpdir(), "check-podfile-lock-"));
after(() => rmSync(scratch, { recursive: true, force: true }));

const check = (...args) => spawnSync(process.execPath, [script, ...args], { encoding: "utf8" });

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
