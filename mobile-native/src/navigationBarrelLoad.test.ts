// The state/navigation barrel must stay loadable by a real-ESM `.mts` tool run
// under tsx (#2470). The package is `"type": "commonjs"`, so tsx loads the
// barrel as CommonJS, and tsx's named-export synthesis cannot see the names
// through an `export *` re-export - `import { relativeAge } from
// "@evener/appwire-client/state/navigation"` then fails at load time with
// "does not provide an export named 'relativeAge'". Vite and Metro resolve the
// star barrel fine, so no other gate noticed; only the scripts path broke.
//
// This runs the same loader a script does, on a probe that imports a value
// through the barrel by absolute path (resolution aside, the transform that
// hides a star re-export's names is identical). Explicit named re-exports are
// what keep the names visible; a return to `export *` fails here.
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const tsxCli = fileURLToPath(
	new URL("../node_modules/tsx/dist/cli.mjs", import.meta.url),
);
const barrel = new URL(
	"../../appwire-client/typescript/state/navigation/index.ts",
	import.meta.url,
).href;

describe("state/navigation barrel under tsx", () => {
	it("exposes its named value exports to a real-ESM .mts script", () => {
		const dir = mkdtempSync(path.join(tmpdir(), "evener-navigation-barrel-"));
		const probe = path.join(dir, "probe.mts");
		writeFileSync(
			probe,
			[
				`import { NavigationBaseInvalidError, NAVIGATION_SECTION_LIMIT, relativeAge } from ${JSON.stringify(barrel)};`,
				'process.stdout.write([typeof relativeAge, NAVIGATION_SECTION_LIMIT, typeof NavigationBaseInvalidError].join(" "));',
				"",
			].join("\n"),
		);
		try {
			const stdout = execFileSync(process.execPath, [tsxCli, probe], {
				encoding: "utf8",
			});
			expect(stdout).toBe("function 50 function");
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});
});
