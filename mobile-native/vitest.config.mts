import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
	test: {
		// Fakes for native modules every suite would otherwise mock alike.
		setupFiles: [fileURLToPath(new URL("./src/vitestSetup.ts", import.meta.url))],
	},
	resolve: {
		// Match Metro's app-owned resolution for the shared headless sources.
		// The @evener/appwire-client entries mirror metro.config.js's own
		// resolveRequest branch: the package is checked in at
		// appwire-client/typescript, never installed, so the name resolves
		// nowhere without an explicit alias. An alias key also matches
		// `<key>/<subpath>`, so the specific entries have to come first or the
		// root entry swallows them.
		alias: {
			"@evener/appwire-client/docContent": fileURLToPath(
				new URL("../appwire-client/typescript/docContent.ts", import.meta.url),
			),
			"@evener/appwire-client/state/navigation": fileURLToPath(
				new URL("../appwire-client/typescript/state/navigation/index.ts", import.meta.url),
			),
			"@evener/appwire-client/state/credentials": fileURLToPath(
				new URL("../appwire-client/typescript/state/credentials/index.ts", import.meta.url),
			),
			"@evener/appwire-client/state/extensions": fileURLToPath(
				new URL("../appwire-client/typescript/state/extensions/index.ts", import.meta.url),
			),
			"@evener/appwire-client/state/mutation": fileURLToPath(
				new URL("../appwire-client/typescript/state/mutation/index.ts", import.meta.url),
			),
			"@evener/appwire-client/state/connection": fileURLToPath(
				new URL("../appwire-client/typescript/state/connection/index.ts", import.meta.url),
			),
			"@evener/appwire-client/testing": fileURLToPath(
				new URL("../appwire-client/typescript/testing", import.meta.url),
			),
			"@evener/appwire-client": fileURLToPath(
				new URL("../appwire-client/typescript/index.ts", import.meta.url),
			),
			// The shared headless sources import react, and a checkout that also
			// has cmd/evener-hub/frontend/node_modules would resolve it to the
			// frontend's copy for a frontend source while the suite holds this
			// app's. Pin it, as Metro does by rewriting a shared source's origin
			// module path (metro.config.js), so one React serves the whole graph
			// (issue #3076).
			react: fileURLToPath(
				new URL("./node_modules/react", import.meta.url),
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
