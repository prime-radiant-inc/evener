import { existsSync, mkdtempSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import ts from "typescript";
// Namespace import, not destructured: the parse guard below is a spy target,
// and a spy only intercepts a call made through the module object.
import * as specifiers from "../../scripts/sdk/module-specifiers.mjs";

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
// RoboRev panel: two ups from this file reach the repo root (the file's own
// "../../scripts/sdk/module-specifiers.mjs" import confirms the depth); three
// overshot to the parent and left the sweep reading nothing.
const repoRoot = path.resolve(path.dirname(self), "../..");
const deletedDir = path.join(repoRoot, "mobile", "src", "conversation");
const deletedSegment = ["conversation", "project"].join("/");
const deletedRoot = ["mobile", "src", "conversation"].join("/");

const APP_TREES = [path.join(repoRoot, "mobile/src"), path.join(repoRoot, "mobile-native/src")];

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
	// A file can only name the deleted directory if its text carries the
	// directory's final segment (a specifier that resolves into
	// ".../conversation" must name that segment) or a backslash escape that
	// could decode to it. A string literal decodes to its raw text unless it
	// holds an escape, and every escape begins with a backslash, so a file
	// with neither cannot name the directory: read it, but do not parse it.
	// Escapes only ever add files to the parsed set, so this fast path never
	// hides an offender — the escape fixture below still fails as before.
	const deletedName = path.basename(deleted);
	for (const tree of trees) {
		for (const file of sourceFiles(tree)) {
			if (file === self) continue;
			const text = readFileSync(file, "utf8");
			if (!text.includes(deletedName) && !text.includes("\\")) continue;
			const source = specifiers.parseSource(ts, file, text);
			for (const site of specifiers.moduleSpecifierSites(ts, source)) {
				const resolved =
					site.text.startsWith(".") || site.text.startsWith("/") ? path.resolve(path.dirname(file), site.text) : null;
				if (
					site.text.includes(deletedSegment) ||
					site.text.includes(deletedRoot) ||
					(resolved !== null && (resolved === deleted || resolved.startsWith(`${deleted}${path.sep}`)))
				) {
					found.push(`${path.relative(tree, file)}: ${site.text}`);
					break;
				}
			}
		}
	}
	return found;
}

// A throwaway app tree under a unique scratch root, for the guard fixtures
// below. Each file in `files` is written under a fresh `mobile/src` root; the
// returned `deleted` is the (absent) conversation directory the guard looks
// for. The unique root (review round 3) keeps concurrent runs — parallel
// worktrees, CI shards on one host — from deleting each other's fixture
// mid-assertion.
function scratchAppTree(files: Record<string, string>): {
	tree: string;
	deleted: string;
	cleanup: () => void;
} {
	const root = mkdtempSync(
		path.join(process.env.EVENER_SCRATCH_DIR ?? process.env.TMPDIR ?? "/tmp", "reachability-fixture-"),
	);
	const tree = path.join(root, "mobile/src");
	for (const [relative, contents] of Object.entries(files)) {
		const full = path.join(tree, relative);
		mkdirSync(path.dirname(full), { recursive: true });
		writeFileSync(full, contents);
	}
	return {
		tree,
		deleted: path.join(root, "mobile", "src", "conversation"),
		cleanup: () => rmSync(root, { recursive: true, force: true }),
	};
}

describe("the deleted private projection family is unreachable", () => {
	it("the sweep reads the repository, not a directory above it", () => {
		// RoboRev panel: repoRoot walked up one level too far, so the swept
		// trees did not exist, sourceFiles swallowed the readdir error, and
		// the headline assertion passed against nothing — the only guard on
		// the deletion could never fail. The sweep must read the real trees.
		for (const tree of APP_TREES) {
			expect(existsSync(tree), `swept tree ${tree}`).toBe(true);
		}
	});

	it("no source in either app tree names mobile/src/conversation/", () => {
		expect(offendersIn(APP_TREES, deletedDir)).toEqual([]);
	});

	it("flags a relative specifier that resolves into the deleted directory", () => {
		// A deleted DIRECTORY is unreachable as a whole: a specifier naming any
		// path inside it (not only the two files it used to hold) must fail
		// the guard. The fixture exercises the resolution the real sweep runs
		// against a smuggled `../conversation/types` — a specifier the old
		// substring-only match slipped.
		const { tree, deleted, cleanup } = scratchAppTree({
			"state/smuggler.ts": `import type { Thing } from "../conversation/types";\n`,
		});
		try {
			expect(offendersIn([tree], deleted)).toEqual(["state/smuggler.ts: ../conversation/types"]);
		} finally {
			cleanup();
		}
	});

	it("skips parsing a file that cannot name the deleted directory", () => {
		// The sweep's runtime scales with the tree because it parsed every file;
		// the guard parses only files whose text could carry a matching
		// specifier. This file has neither the directory's segment nor a
		// backslash escape, so it is never parsed.
		const { tree, deleted, cleanup } = scratchAppTree({
			"state/unrelated.ts": `export const unrelated = 1;\n`,
		});
		const parse = vi.spyOn(specifiers, "parseSource");
		try {
			expect(offendersIn([tree], deleted)).toEqual([]);
			expect(parse).not.toHaveBeenCalled();
		} finally {
			parse.mockRestore();
			cleanup();
		}
	});

	it("still parses a file whose specifier hides the segment behind an escape", () => {
		// The fast path keeps any file carrying a backslash, because an escape
		// decodes to text the raw bytes do not spell. `\u0061` is `a`, so this
		// import names "../conversation/types" while its raw text never says
		// "conversation" — the guard must still see the offender.
		const { tree, deleted, cleanup } = scratchAppTree({
			"state/smuggler.ts": `import type { Thing } from "../convers\\u0061tion/types";\n`,
		});
		try {
			expect(offendersIn([tree], deleted)).toEqual(["state/smuggler.ts: ../conversation/types"]);
		} finally {
			cleanup();
		}
	});
});
