const path = require("node:path");
const { getDefaultConfig } = require("expo/metro-config");
const config = getDefaultConfig(__dirname);
const root = path.resolve(__dirname, "..");
config.watchFolders = [root];
config.resolver.nodeModulesPaths = [path.resolve(__dirname, "node_modules")];
// Shared headless sources use this app's React/Zustand installation. Keep
// normal resolution inside native dependencies, including nested packages.
config.resolver.resolveRequest = (context, name, platform) => {
	const shared =
		context.originModulePath.startsWith(path.join(root, "mobile", "src")) ||
		context.originModulePath.startsWith(
			path.join(root, "cmd", "evener-hub", "frontend", "src"),
		);
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
