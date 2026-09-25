import { readdirSync, readFileSync } from "node:fs";
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
// The needle is assembled rather than spelled so this file cannot match
// itself: the guard is a rule about every OTHER file, and a quoted literal
// here would be the one quoted specifier in the tree naming the dead path.
const deletedSegment = ["conversation", "project"].join("/");
const deletedRoot = ["mobile", "src", "conversation"].join("/");
const self = path.resolve(fileURLToPath(import.meta.url));

const TREES = [
	path.resolve(path.dirname(self), "../../mobile/src"),
	path.resolve(path.dirname(self), "../../mobile-native/src"),
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

function offenders(): string[] {
	const found: string[] = [];
	for (const tree of TREES) {
		for (const file of sourceFiles(tree)) {
			if (file === self) continue;
			const source = parseSource(ts, file, readFileSync(file, "utf8"));
			for (const site of moduleSpecifierSites(ts, source)) {
				if (
					site.text.includes(deletedSegment) ||
					site.text.includes(deletedRoot)
				) {
					found.push(`${path.relative(process.cwd(), file)}: ${site.text}`);
					break;
				}
			}
		}
	}
	return found;
}

describe("the deleted private projection family is unreachable", () => {
	it("no source in either app tree names mobile/src/conversation/", () => {
		expect(offenders()).toEqual([]);
	});
});
