// Prove that every scripts/*.mts tool can still load its own module graph.
//
// These scripts are run by hand from the README (`npx tsx scripts/check-hub.mts
// ORIGIN TOKEN_FILE`, `npm exec -- tsx scripts/demo-hub.mts`) and by no gate,
// so nothing noticed when their graph stopped resolving. That is not
// hypothetical: they reach app modules that import @evener/appwire-client by
// name, tsx reads tsconfig.json and never tsconfig.check.json, and until the
// paths landed in tsconfig.json every one of these scripts died on
// `Cannot find module '@evener/appwire-client'` while `npm run check` stayed
// green.
//
// Resolution is what is checked, not execution: each of these scripts opens a
// socket or starts a server the moment its body runs, so importing them is not
// something a gate can do. Node's resolver is asked directly instead, through
// the same createRequire path tsx hooks, and the graph is walked from each
// script down to the first node_modules boundary.
//
// Statement-level `import type` is skipped: it is erased before anything runs,
// so it is not part of what has to resolve. Everything else is followed,
// `export * from` included -- it names no binding, but it is loaded, and a
// walk that stops there stops one hop short of whatever the chain reaches.
import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import ts from "typescript";
import { isLoadedAtRuntime, moduleSpecifierSites, parseSource, walkImportGraph } from "../../scripts/sdk/module-specifiers.mjs";

const self = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(self);
const repoRoot = path.resolve(scriptsDir, "..", "..");

function runtimeSpecifiers(file: string): string[] {
	return moduleSpecifierSites(ts, parseSource(ts, file, readFileSync(file, "utf8")))
		.filter(isLoadedAtRuntime)
		.map((site) => site.text);
}

export interface ResolutionFailure {
	file: string;
	specifier: string;
	reason: string;
}

/** Resolves `specifier` as read from `from`, or throws the way Node does. */
export type Resolve = (from: string, specifier: string) => string;

// tsx patches Node's CommonJS resolver to know TypeScript extensions, which is
// the whole reason this check runs under tsx. It is a parameter so a test can
// exercise which sites the walk follows without standing up that patch; what
// tsx actually resolves is what the gate itself answers, every run.
const resolveThroughTsx: Resolve = (from, specifier) => createRequire(from).resolve(specifier);

export interface WalkResult {
	failures: ResolutionFailure[];
	/** First-party modules reached, the scripts themselves included. */
	reached: string[];
}

// Walk each entry's module graph, resolving every specifier the way tsx does
// and stopping at the first node_modules boundary: what is inside a dependency
// is not this gate's business. The graph walk itself is shared with the
// package-test gate; the resolve-and-record is the part particular to this one.
const inNodeModules = (file: string) => file.includes(`${path.sep}node_modules${path.sep}`);

export function walkScriptImports(entries: string[], repoRoot: string, resolve: Resolve = resolveThroughTsx): WalkResult {
	const failures: ResolutionFailure[] = [];
	const reached = walkImportGraph(
		entries,
		(file: string) => runtimeSpecifiers(file),
		(file: string, specifier: string) => {
			if (specifier.startsWith("node:")) return null;
			let resolved: string;
			try {
				resolved = resolve(file, specifier);
			} catch (error) {
				failures.push({
					file: path.relative(repoRoot, file),
					specifier,
					reason: (error as Error).message.split("\n")[0],
				});
				return null;
			}
			// Followed only when it lands on a first-party file; a dependency's
			// own graph is not this gate's business.
			return path.isAbsolute(resolved) && !inNodeModules(resolved) ? resolved : null;
		},
	);
	return { failures, reached: [...reached] };
}

// The scripts/*.mts tools under `dir`, excluding this checker itself.
export function scriptEntries(dir: string, self: string): string[] {
	return readdirSync(dir)
		.filter((name) => name.endsWith(".mts"))
		.map((name) => path.join(dir, name))
		.filter((file) => file !== self);
}

// Guarded so a test can import the walk without running the check.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
	const rootIndex = process.argv.indexOf("--root");
	const dir = rootIndex === -1 ? scriptsDir : path.resolve(process.argv[rootIndex + 1] ?? "");
	const entries = scriptEntries(dir, self);
	if (entries.length === 0) throw new Error(`no scripts/*.mts tools found under ${dir}`);
	const { failures, reached } = walkScriptImports(entries, repoRoot);
	if (failures.length > 0) {
		console.error(`${failures.length} import(s) in the scripts/*.mts tools do not resolve under tsx:`);
		for (const failure of failures) console.error(`  ${failure.file} imports ${failure.specifier}: ${failure.reason}`);
		console.error("tsx reads mobile-native/tsconfig.json; tsconfig.check.json is only read by `npm run check`.");
		process.exit(1);
	}
	console.log(`${entries.length} scripts/*.mts tools resolve, across ${reached.length} first-party modules`);
}
