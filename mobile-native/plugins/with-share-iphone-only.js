// The share extension must stay iPhone-only like its host app. The
// Apple-targets generator defaults generated extension targets to
// TARGETED_DEVICE_FAMILY "1,2", and its repeat update replaces the target's
// build configuration list. Mods for the same name run in reverse
// registration order, so this is registered before "@bacons/apple-targets"
// in app.config.js and runs after the generator creates or updates the
// target.
const { PBXNativeTarget } = require("@bacons/xcode");
// The composition API for the same Xcode-project mod the generator uses.
const { withXcodeProjectBeta } = require("@bacons/apple-targets/build/with-bacons-xcode");

module.exports = (config) =>
	withXcodeProjectBeta(config, (config) => {
		for (const target of config.modResults.rootObject.props.targets) {
			if (!PBXNativeTarget.is(target) || target.props.name !== "EvenerShareExtension") {
				continue;
			}
			// Sets the setting on every build configuration of the target.
			target.setBuildSetting("TARGETED_DEVICE_FAMILY", "1");
		}
		return config;
	});
