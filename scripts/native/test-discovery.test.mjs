// Prove the native test runner's discovery is scoped to the checkout whose
// config it loads. Vitest does not read .gitignore, and this repository keeps
// other sessions' full checkout copies in gitignored directories beside the
// checkout (`.worktrees/` lanes, `.claude/worktrees/` agent worktrees), so a
// run rooted above mobile-native — the shape `npm run test:shared` used with
// `--root ..` before that script was folded away — collected those lanes'
// suites too, and the lanes without their own node_modules failed wholesale
// at transform: 16 suite-level failures with zero test failures (issue #5469).
// The exclusion in mobile-native/vitest.config.mts is what stops that, so
// this drives the real vitest binary with the real config against a fixture
// tree shaped like a checkout holding two foreign lanes: dropping or mistyping
// the exclusion fails here instead of resurfacing as foreign suites in
// someone's gate run.
//
// Needs the installed mobile-native/node_modules, exactly like the
// run-tests.sh stage it guards; `npm test` runs it (node --test).
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const mobileNative = path.join(here, "../../mobile-native");
const config = path.join(mobileNative, "vitest.config.mts");
const vitestBin = path.join(mobileNative, "node_modules/.bin/vitest");
const scratch = mkdtempSync(path.join(tmpdir(), "native-test-discovery-"));
after(() => rmSync(scratch, { recursive: true, force: true }));

const writeSuite = (root, relative) => {
	const file = path.join(root, relative);
	mkdirSync(path.dirname(file), { recursive: true });
	writeFileSync(file, 'import { test } from "vitest";\n\ntest("collected", () => {});\n');
	return file;
};

// A fixture checkout: one in-tree suite the run must keep, plus a suite in
// each foreign-lane directory a root above mobile-native can reach.
const checkout = mkdtempSync(path.join(scratch, "checkout-"));
writeSuite(checkout, "mobile-native/src/checkoutSuite.test.ts");
const foreignLanes = [
	".worktrees/fake-lane/mobile-native/src/foreignSuite.test.ts",
	".claude/worktrees/fake-lane/mobile-native/src/foreignSuite.test.ts",
];
for (const lane of foreignLanes) writeSuite(checkout, lane);

test("a root above mobile-native collects the checkout's suites but no foreign lane's", () => {
	const run = spawnSync(
		vitestBin,
		[
			"list",
			"--filesOnly",
			// The config loader that never writes a bundled copy into
			// node_modules/.vite-temp (issue #4193), the way the frontend's
			// own vitest spawns run.
			"--configLoader",
			"runner",
			"--root",
			checkout,
			"--config",
			config,
			// A path filter shaped like test:shared's `mobile/src`: it matches
			// the in-tree suite and the lanes' copies of it alike.
			"mobile-native/src",
		],
		{ cwd: mobileNative, encoding: "utf8", env: { ...process.env, NODE_DISABLE_COMPILE_CACHE: "1" } },
	);
	assert.equal(run.status, 0, run.error ?? (run.stderr || run.stdout));
	const listed = run.stdout.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
	assert.ok(
		listed.some((line) => line.endsWith(path.join("mobile-native", "src", "checkoutSuite.test.ts"))),
		`discovery lost the checkout's own suite:\n${listed.join("\n")}`,
	);
	const collected = listed.filter((line) => line.includes(".worktrees/") || line.includes(".claude/worktrees/"));
	assert.deepEqual(
		collected.map((line) => line.split(path.sep).slice(-3).join(path.sep)),
		[],
		"discovery collected foreign worktree lanes' suites",
	);
});
