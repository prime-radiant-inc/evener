import {
	mkdtempSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import ts from "typescript";
import {
	moduleSpecifierSites,
	parseSource,
} from "../../scripts/sdk/module-specifiers.mjs";

// D24-6 deleted the private projection family: mobile/src/conversation/ held
// the seam's pre-shared-projector rows (project.ts, project.test.ts), and the
// shared-projector row adapter (projectedRows.ts) is the canonical home of the
// row vocabulary and the timeline projection now. No source in either app tree
// may name the deleted directory again: a specifier that does resolves to a
// file that no longer exists, and every consumer that still compiles does so
// only because a stale copy of the family is still reachable some other way.
//
// The sweep reads specifier sites through the repo's own TypeScript reader
// (scripts/sdk/module-specifiers.mjs) rather than grepping, so every form is
// covered: import, import type, dynamic import(), the inline
// import("../../...").Type annotations, require(), re-exports and vi.mock()
// paths. Type-only sites are just as forbidden as runtime ones: the family is
// deleted, so even an erased import names a module that is not there.
//
// A relative specifier names the deleted directory when RESOLVING it against
// the importing file's directory lands inside it — a bare substring match
// would let `../conversation/anything-but-project` slip the guard (the
// directory is deleted as a whole, not just one file in it).
//
// The needles are assembled rather than spelled so this file cannot match
// itself: the guard is a rule about every OTHER file, and a quoted literal
// here would be the one quoted specifier in the tree naming the dead path.
const self = path.resolve(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(path.dirname(self), "../../..");
const deletedDir = path.join(repoRoot, "mobile", "src", "conversation");
const deletedSegment = ["conversation", "project"].join("/");
const deletedRoot = ["mobile", "src", "conversation"].join("/");

const APP_TREES = [
	path.join(repoRoot, "mobile/src"),
	path.join(repoRoot, "mobile-native/src"),
];

function* sourceFiles(dir: string): Generator<string> {
	let entries;
	try {
		entries = readdirSync(dir, { withFileTypes: true });
	} catch {
		return;
	}
	for (const entry of entries) {
		if (entry.name === "node_modules") continue;
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) yield* sourceFiles(full);
		else if (/\.(ts|tsx|mts|cts|js|jsx|mjs|cjs)$/.test(entry.name)) yield full;
	}
}

// Every file under `trees` whose module specifiers name the directory
// `deleted` (relative specifiers resolved against the importing file):
// "<file>: <specifier>" per offender, first hit per file.
function offendersIn(trees: readonly string[], deleted: string): string[] {
	const found: string[] = [];
	for (const tree of trees) {
		for (const file of sourceFiles(tree)) {
			if (file === self) continue;
			const source = parseSource(ts, file, readFileSync(file, "utf8"));
			for (const site of moduleSpecifierSites(ts, source)) {
				const resolved =
					site.text.startsWith(".") || site.text.startsWith("/")
						? path.resolve(path.dirname(file), site.text)
						: null;
				if (
					site.text.includes(deletedSegment) ||
					site.text.includes(deletedRoot) ||
					(resolved !== null &&
						(resolved === deleted || resolved.startsWith(`${deleted}${path.sep}`)))
				) {
					found.push(`${path.relative(tree, file)}: ${site.text}`);
					break;
				}
			}
		}
	}
	return found;
}

describe("the deleted private projection family is unreachable", () => {
	it("no source in either app tree names mobile/src/conversation/", () => {
		expect(offendersIn(APP_TREES, deletedDir)).toEqual([]);
	});

	it("flags a relative specifier that resolves into the deleted directory", () => {
		// A deleted DIRECTORY is unreachable as a whole: a specifier naming any
		// path inside it (not only the two files it used to hold) must fail
		// the guard. The fixture exercises the resolution the real sweep runs
		// against a smuggled `../conversation/types` — a specifier the old
		// substring-only match slipped.
		// A unique root (review round 3): concurrent runs share the scratch
		// dir — parallel worktrees, CI shards on one host — and a fixed name
		// lets one run's cleanup delete another run's fixture mid-assertion.
		const root = mkdtempSync(
			path.join(
				process.env.EVENER_SCRATCH_DIR ?? process.env.TMPDIR ?? "/tmp",
				"reachability-fixture-",
			),
		);
		const stateDir = path.join(root, "mobile", "src", "state");
		mkdirSync(stateDir, { recursive: true });
		writeFileSync(
			path.join(stateDir, "smuggler.ts"),
			`import type { Thing } from "../conversation/types";\n`,
		);
		try {
			expect(
				offendersIn(
					[path.join(root, "mobile/src")],
					path.join(root, "mobile", "src", "conversation"),
				),
			).toEqual(["state/smuggler.ts: ../conversation/types"]);
		} finally {
			rmSync(root, { recursive: true, force: true });
		}
	});
});
