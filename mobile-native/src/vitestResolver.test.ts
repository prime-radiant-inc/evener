// vitest.config.mts pins the bare dependencies the shared headless sources
// import to this app's own install. mobile-native's suites load mobile/src and
// cmd/evener-hub/frontend/src, and a checkout that also has the frontend's
// node_modules would otherwise resolve a frontend source's `react` to the
// frontend's copy while the suite holds this app's, putting two React copies in
// one module graph and failing every component test that mounts a hook from a
// shared source (issue #3076). metro.config.js already does the equivalent for
// the bundle by rewriting a shared source's origin module path to this app's
// entry point; Vitest reads neither tsconfig paths nor metro.config.js, so the
// alias here is the only pin.
//
// The config is loaded through Vite rather than imported as a `.mts` module: a
// bare `import "../vitest.config.mts"` is a type error under the native
// typecheck gate's tsconfig, which does not allow importing TypeScript
// extensions.
import { existsSync, realpathSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { loadConfigFromFile } from "vite";
import { describe, expect, it } from "vitest";

const configDir = fileURLToPath(new URL("..", import.meta.url));

async function aliasMap(): Promise<Record<string, string>> {
	const loaded = await loadConfigFromFile(
		{ command: "serve", mode: "test", isSsrBuild: false, isPreview: false },
		path.join(configDir, "vitest.config.mts"),
		configDir,
	);
	const alias = loaded?.config.resolve?.alias;
	if (!alias || Array.isArray(alias))
		throw new Error("vitest.config.mts's resolve.alias is not the object map this test reads");
	return alias as Record<string, string>;
}

// The directory the suite's own `react` imports resolve to, found by walking up
// from the resolved entry file, so the comparison holds whether the package's
// main sits at its root or in a subdirectory.
function reactPackageDir(): string {
	let dir = path.dirname(createRequire(import.meta.url).resolve("react"));
	while (dir !== path.dirname(dir)) {
		if (path.basename(dir) === "react" && existsSync(path.join(dir, "package.json"))) return dir;
		dir = path.dirname(dir);
	}
	throw new Error("react's package directory was not found above its entry file");
}

describe("vitest.config.mts pins the shared sources' React to this app", () => {
	it("aliases react to the copy the suite's own imports resolve to", async () => {
		const target = (await aliasMap()).react;
		expect(target, "vitest.config.mts aliases react").toBeTruthy();
		expect(existsSync(target)).toBe(true);
		expect(realpathSync(target)).toBe(realpathSync(reactPackageDir()));
	});
});
