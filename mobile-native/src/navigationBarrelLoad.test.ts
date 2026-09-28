// The state/navigation barrel must stay loadable by a real-ESM `.mts` tool run
// under tsx (#2470). The package is `"type": "commonjs"`, so tsx loads the
// barrel as CommonJS, and tsx's named-export synthesis cannot see the names
// through an `export *` re-export - `import { relativeAge } from
// "@evener/appwire-client/state/navigation"` then fails at load time with
// "does not provide an export named 'relativeAge'". Vite and Metro resolve the
// star barrel, so no other gate noticed; only the scripts path broke.
//
// This runs the same loader a script does, on a probe that imports one value
// from every module the barrel publishes. Explicit named re-exports are what
// keep the names visible; a return to `export *` fails here, and so does
// dropping a module's re-export, because its probe name goes missing.
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const tsxCli = fileURLToPath(
	new URL("../node_modules/tsx/dist/cli.mjs", import.meta.url),
);
// tsx reads this file (not tsconfig.check.json) for the package-name paths the
// scripts/*.mts tools rely on, so the probe resolves the barrel the same way.
const tsConfig = fileURLToPath(new URL("../tsconfig.json", import.meta.url));

// One value export from each of the barrel's nine modules. A module whose
// re-export is dropped makes its name an unresolved import, which fails the
// probe the way the original bug did.
const probeSource = [
	"import {",
	"\tapplyDelta,",
	"\tcanonicalHostId,",
	"\tcreateNavigationStore,",
	"\tdecodeNavigationResponse,",
	"\tequalJSON,",
	"\tisSequenceGap,",
	"\tkeyID,",
	"\tNAVIGATION_SECTION_LIMIT,",
	"\tNavigationRevalidator,",
	"\trelativeAge,",
	'} from "@evener/appwire-client/state/navigation";',
	"process.stdout.write(",
	"\tJSON.stringify({",
	"\t\tcodec: typeof decodeNavigationResponse,",
	"\t\thostGrouping: typeof canonicalHostId,",
	"\t\timmutable: typeof equalJSON,",
	"\t\tinvalidation: typeof isSequenceGap,",
	"\t\tmerge: typeof applyDelta,",
	"\t\trevalidator: typeof NavigationRevalidator,",
	"\t\tselectors: typeof relativeAge,",
	"\t\tstore: typeof createNavigationStore,",
	"\t\ttypes: typeof keyID,",
	'\t\tlimitIsNumber: typeof NAVIGATION_SECTION_LIMIT === "number",',
	"\t}),",
	");",
	"",
].join("\n");

describe("state/navigation barrel under tsx", () => {
	it("exposes a value from every module to a real-ESM .mts script", () => {
		const dir = mkdtempSync(path.join(tmpdir(), "evener-navigation-barrel-"));
		const probe = path.join(dir, "probe.mts");
		writeFileSync(probe, probeSource);
		try {
			const stdout = execFileSync(
				process.execPath,
				[tsxCli, "--tsconfig", tsConfig, probe],
				{ encoding: "utf8" },
			);
			expect(JSON.parse(stdout)).toEqual({
				codec: "function",
				hostGrouping: "function",
				immutable: "function",
				invalidation: "function",
				merge: "function",
				revalidator: "function",
				selectors: "function",
				store: "function",
				types: "function",
				limitIsNumber: true,
			});
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});
});
