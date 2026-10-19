const fs = require("node:fs");
const path = require("node:path");
const { withAppDelegate, withInfoPlist } = require("expo/config-plugins");

// Keep the generated factory and other plugins' extension points. Each owned
// section is replaceable on later prebuilds, including after editing the adapter.
function replaceSection(contents, name, original, replacement) {
	const begin = `// @generated begin evener-${name}`;
	const end = `// @generated end evener-${name}`;
	const start = contents.indexOf(begin);
	let input = original;
	if (start !== -1) {
		const finish = contents.indexOf(end, start);
		if (finish === -1) throw new Error(`Incomplete iOS scene section: ${name}`);
		input = contents.slice(start, finish + end.length);
	}
	if (!input || contents.split(input).length !== 2) {
		throw new Error(`Cannot update iOS scene ${name}. Review the Expo AppDelegate template before prebuilding.`);
	}
	return contents.replace(input, `${begin}\n${replacement}\n${end}`);
}

module.exports = (config) => {
	config = withInfoPlist(config, (config) => {
		config.modResults.UIApplicationSceneManifest = {
			UIApplicationSupportsMultipleScenes: false,
			UISceneConfigurations: {
				UIWindowSceneSessionRoleApplication: [
					{
						UISceneConfigurationName: "Evener",
						UISceneDelegateClassName: "$(PRODUCT_MODULE_NAME).SceneDelegate",
					},
				],
			},
		};
		return config;
	});
	return withAppDelegate(config, (config) => {
		if (config.modResults.language !== "swift") {
			throw new Error("Evener's iOS scene integration requires a Swift AppDelegate.");
		}
		let contents = config.modResults.contents;
		contents = replaceSection(
			contents,
			"scene-state",
			"  var window: UIWindow?",
			`  weak var window: UIWindow?
  var sceneLaunchOptions: [UIApplication.LaunchOptionsKey: Any]?
  var sceneRootViewController: UIViewController?`,
		);
		contents = replaceSection(
			contents,
			"scene-startup",
			`#if os(iOS) || os(tvOS)
    window = UIWindow(frame: UIScreen.main.bounds)
    factory.startReactNative(
      withModuleName: "main",
      in: window,
      launchOptions: launchOptions)
#endif`,
			"    sceneLaunchOptions = launchOptions",
		);
		contents = replaceSection(
			contents,
			"scene-url",
			"    return super.application(app, open: url, options: options) || RCTLinkingManager.application(app, open: url, options: options)",
			`    let expoHandled = super.application(app, open: url, options: options)
    let reactHandled = RCTLinkingManager.application(app, open: url, options: options)
    return expoHandled || reactHandled`,
		);
		const adapter = fs.readFileSync(path.join(__dirname, "ios-scene-delegate.swift"), "utf8").trimEnd();
		if (!contents.includes("// @generated begin evener-scene-adapter")) {
			contents = `${contents.trimEnd()}\n\n// evener-scene-adapter\n`;
		}
		contents = replaceSection(contents, "scene-adapter", "// evener-scene-adapter", adapter);
		config.modResults.contents = contents;
		return config;
	});
};
