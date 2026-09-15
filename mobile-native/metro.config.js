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
// the native bundle - and `make test-native-bundle` bundles the app, so a break
// here fails CI.
const appwirePackage = path.join(root, "appwire-client", "typescript");
const appwireName = "@evener/appwire-client";
// The source extensions to probe for a package subpath, from the one shared
// list scripts/sdk/source-files.env, so this resolver stays level with the
// rewriter and the gate. A .tsx module (a widget, say) resolves like a .ts one.
const sharedEnv = fs.readFileSync(path.join(root, "scripts", "sdk", "source-files.env"), "utf8");
const sourceExtensions = (sharedEnv.match(/^extensions="([^"]*)"/m)?.[1] ?? "").split(/\s+/).filter(Boolean);
// Shared headless sources use this app's React/Zustand installation. Keep
// normal resolution inside native dependencies, including nested packages.
config.resolver.resolveRequest = (context, name, platform) => {
	const shared =
		context.originModulePath.startsWith(path.join(root, "mobile", "src")) ||
		context.originModulePath.startsWith(
			path.join(root, "cmd", "evener-hub", "frontend", "src"),
		) ||
		context.originModulePath.startsWith(appwirePackage);
	// Every native import of the package comes through here -- the whole tree
	// spells it by name now -- and `make test-native-bundle` is what exercises
	// it: vitest resolves the name through this app's vitest config and `tsc`
	// through tsconfig.check.json's paths, so a break in this branch passes
	// both and would otherwise surface first on a device.
	if (name === appwireName || name.startsWith(`${appwireName}/`)) {
		const subpath = name.slice(appwireName.length).replace(/^\//, "") || "index";
		// The first extension that exists wins. A subpath the package does not
		// have falls through rather than resolving to a file that is not there:
		// Metro's own "unable to resolve" names the specifier and the import
		// stack, where a missing sourceFile surfaces later as a read error
		// against a path the author never wrote.
		for (const extension of sourceExtensions) {
			const filePath = path.join(appwirePackage, `${subpath}.${extension}`);
			if (fs.existsSync(filePath)) {
				return { type: "sourceFile", filePath };
			}
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
