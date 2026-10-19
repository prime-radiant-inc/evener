const { withDangerousMod } = require("expo/config-plugins");

// Refuses an iOS prebuild without an Apple team id, in one place the CLI's
// argument spelling cannot get around. The share extension target is generated
// from targets/share by @bacons/apple-targets, which requires the id; without
// it the plugin is left out and the generated project silently has no share
// extension. Every non-prebuild use of the config (typecheck, Metro, the
// Android project) still loads.
module.exports = (config) =>
  withDangerousMod(config, [
    "ios",
    async (config) => {
      if (!config.ios?.appleTeamId) {
        throw new Error(
          "APPLE_TEAM_ID is not set. The iOS share extension is generated at prebuild time and needs the Apple team id; export APPLE_TEAM_ID before prebuilding iOS.",
        );
      }
      return config;
    },
  ]);
