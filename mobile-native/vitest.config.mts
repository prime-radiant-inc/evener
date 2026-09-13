import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
	resolve: {
		// Match Metro's app-owned resolution for the shared headless sources.
		// The @evener/appwire-client entries mirror metro.config.js's own
		// resolveRequest branch: the package is checked in at
		// appwire-client/typescript, never installed, so the name resolves
		// nowhere without an explicit alias. An alias key also matches
		// `<key>/<subpath>`, so the two specific entries have to come first or
		// the root entry swallows them.
		alias: {
			"@evener/appwire-client/docContent": fileURLToPath(
				new URL("../appwire-client/typescript/docContent.ts", import.meta.url),
			),
			"@evener/appwire-client/testing": fileURLToPath(
				new URL("../appwire-client/typescript/testing", import.meta.url),
			),
			"@evener/appwire-client": fileURLToPath(
				new URL("../appwire-client/typescript/index.ts", import.meta.url),
			),
			zustand: fileURLToPath(
				new URL("./node_modules/zustand", import.meta.url),
			),
			anser: fileURLToPath(
				new URL("./node_modules/anser/lib/index.js", import.meta.url),
			),
			tinykeys: fileURLToPath(
				new URL("./node_modules/tinykeys/dist/tinykeys.mjs", import.meta.url),
			),
		},
	},
});
