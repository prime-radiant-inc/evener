import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
	resolve: {
		// Match Metro's app-owned resolution for the shared headless sources.
		alias: {
			anser: fileURLToPath(
				new URL("./node_modules/anser/lib/index.js", import.meta.url),
			),
			tinykeys: fileURLToPath(
				new URL("./node_modules/tinykeys/dist/tinykeys.mjs", import.meta.url),
			),
		},
	},
});
