const fs = require("node:fs");
const path = require("node:path");
const { getDefaultConfig } = require("expo/metro-config");
const config = getDefaultConfig(__dirname);
const root = path.resolve(__dirname, "..");
config.watchFolders = [root];
config.resolver.nodeModulesPaths = [path.resolve(__dirname, "node_modules")];
// The AppWire package is checked in, never installed, so Metro has to be told
// both where "@evener/appwire-client" lives and that its sources count as
// shared headless code. Metro reads neither tsconfig paths nor the package's
// own exports map, so this branch is the only thing that resolves the name for
// the native bundle - and no gate bundles the app, so a break here ships green.
const appwirePackage = path.join(root, "appwire-client", "typescript");
const appwireName = "@evener/appwire-client";
// Shared headless sources use this app's React/Zustand installation. Keep
// normal resolution inside native dependencies, including nested packages.
config.resolver.resolveRequest = (context, name, platform) => {
	const shared =
		context.originModulePath.startsWith(path.join(root, "mobile", "src")) ||
		context.originModulePath.startsWith(
			path.join(root, "cmd", "evener-hub", "frontend", "src"),
		) ||
		context.originModulePath.startsWith(appwirePackage);
	// Every native import of the package comes through here, and nothing in CI
	// reads it: vitest resolves the name through this app's vitest config and
	// `tsc` through tsconfig.check.json's paths, so a break in this branch
	// passes both and surfaces first on a device. #1244 is the bundling gate
	// that will exercise it.
	if (name === appwireName || name.startsWith(`${appwireName}/`)) {
		const subpath = name.slice(appwireName.length).replace(/^\//, "");
		const filePath = path.join(appwirePackage, `${subpath || "index"}.ts`);
		// A subpath the package does not have falls through rather than
		// resolving to a file that is not there: Metro's own "unable to
		// resolve" names the specifier and the import stack, where a missing
		// sourceFile surfaces later as a read error against a path the author
		// never wrote.
		if (fs.existsSync(filePath)) {
			return { type: "sourceFile", filePath };
		}
	}
	const originModulePath =
		shared && !name.startsWith(".") && !path.isAbsolute(name)
			? path.join(__dirname, "index.ts")
			: context.originModulePath;
	return context.resolveRequest(
		{ ...context, originModulePath },
		name,
		platform,
	);
};
module.exports = config;
