// The state/navigation barrel must stay loadable by a real-ESM `.mts` tool run
// under tsx (#2470). The package is `"type": "commonjs"`, so tsx loads the
// barrel as CommonJS, and tsx's named-export synthesis cannot see the names
// through an `export *` re-export - `import { relativeAge } from
// "@evener/appwire-client/state/navigation"` then fails at load time with
// "does not provide an export named 'relativeAge'". Vite and Metro resolve the
// star barrel, so no other gate noticed; only the scripts path broke.
//
// The probe loads the barrel and each of its nine modules through the same tsx
// a script uses, then compares the barrel's runtime exports against the union
// of the modules' own. Explicit named re-exports make the two equal; a return
// to `export *` loses every name (the probe's import fails outright), and
// dropping one module's re-export leaves the barrel short of the union.
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const tsxCli = fileURLToPath(
	new URL("../node_modules/tsx/dist/cli.mjs", import.meta.url),
);
// tsx reads this file (not tsconfig.check.json) for the package-name paths the
// scripts/*.mts tools rely on, so the probe resolves the barrel the same way,
// and its paths entry is also how this test finds the modules behind it.
const tsConfig = fileURLToPath(new URL("../tsconfig.json", import.meta.url));

// The barrel's modules, in the order index.ts publishes them.
const moduleNames = [
	"codec",
	"hostGrouping",
	"immutable",
	"invalidation",
	"merge",
	"revalidator",
	"selectors",
	"store",
	"types",
] as const;

function navigationModuleDir(): string {
	const { config, error } = ts.readConfigFile(tsConfig, (file) =>
		readFileSync(file, "utf8"),
	);
	if (error)
		throw new Error(ts.flattenDiagnosticMessageText(error.messageText, "\n"));
	const mapped =
		config?.compilerOptions?.paths?.[
			"@evener/appwire-client/state/navigation"
		]?.[0];
	if (typeof mapped !== "string") {
		throw new Error(
			"tsconfig.json no longer maps @evener/appwire-client/state/navigation",
		);
	}
	return path.dirname(path.resolve(path.dirname(tsConfig), mapped));
}

function probeSource(moduleDir: string): string {
	const moduleImports = moduleNames.map(
		(name) =>
			`import * as ${name} from ${JSON.stringify(pathToFileURL(path.join(moduleDir, `${name}.ts`)).href)};`,
	);
	return [
		`import * as barrel from "@evener/appwire-client/state/navigation";`,
		...moduleImports,
		`const names = (m) => Object.keys(m).filter((key) => key !== "default");`,
		`const union = new Set([${moduleNames.map((name) => `...names(${name})`).join(", ")}]);`,
		`process.stdout.write(JSON.stringify({ barrel: names(barrel).sort(), union: [...union].sort() }));`,
		"",
	].join("\n");
}

describe("state/navigation barrel under tsx", () => {
	it("exposes every module's exported value to a real-ESM .mts script", () => {
		const dir = mkdtempSync(path.join(tmpdir(), "evener-navigation-barrel-"));
		const probe = path.join(dir, "probe.mts");
		writeFileSync(probe, probeSource(navigationModuleDir()));
		try {
			const stdout = execFileSync(
				process.execPath,
				[tsxCli, "--tsconfig", tsConfig, probe],
				{ encoding: "utf8" },
			);
			const { barrel, union } = JSON.parse(stdout) as {
				barrel: string[];
				union: string[];
			};
			expect(union.length).toBeGreaterThan(0);
			expect(barrel).toEqual(union);
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});
});
