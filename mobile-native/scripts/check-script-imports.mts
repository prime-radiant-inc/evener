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
// so it is not part of what has to resolve.
import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { isRuntimeSite, moduleSpecifierSites, parseSource } from "../../scripts/sdk/module-specifiers.mjs";

const self = fileURLToPath(import.meta.url);
const scriptsDir = path.dirname(self);
const repoRoot = path.resolve(scriptsDir, "..", "..");

function runtimeSpecifiers(file: string): string[] {
	return moduleSpecifierSites(ts, parseSource(ts, file, readFileSync(file, "utf8")))
		.filter(isRuntimeSite)
		.map((site) => site.text);
}

const walked = new Set<string>();
const failures: string[] = [];

function walk(file: string) {
	if (walked.has(file)) return;
	walked.add(file);
	if (file.includes(`${path.sep}node_modules${path.sep}`)) return;
	const resolveFrom = createRequire(file);
	for (const specifier of runtimeSpecifiers(file)) {
		if (specifier.startsWith("node:")) continue;
		let resolved: string;
		try {
			resolved = resolveFrom.resolve(specifier);
		} catch (error) {
			failures.push(`${path.relative(repoRoot, file)} imports ${specifier}: ${(error as Error).message.split("\n")[0]}`);
			continue;
		}
		if (path.isAbsolute(resolved)) walk(resolved);
	}
}

const entries = readdirSync(scriptsDir)
	.filter((name) => name.endsWith(".mts"))
	.map((name) => path.join(scriptsDir, name))
	.filter((file) => file !== self);
if (entries.length === 0) throw new Error(`no scripts/*.mts tools found under ${scriptsDir}`);
for (const entry of entries) walk(entry);

if (failures.length > 0) {
	console.error(`${failures.length} import(s) in the scripts/*.mts tools do not resolve under tsx:`);
	for (const failure of failures) console.error(`  ${failure}`);
	console.error("tsx reads mobile-native/tsconfig.json; tsconfig.check.json is only read by `npm run check`.");
	process.exit(1);
}
const reached = [...walked].filter((file) => !file.includes(`${path.sep}node_modules${path.sep}`)).length;
console.log(`${entries.length} scripts/*.mts tools resolve, across ${reached} first-party modules`);
