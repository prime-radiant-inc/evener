const { withAppBuildGradle } = require("expo/config-plugins");

module.exports = (config) =>
  withAppBuildGradle(config, (config) => {
    const directive = 'apply from: "../../plugins/shared-sources.gradle"';
    if (!config.modResults.contents.includes(directive)) {
      config.modResults.contents += `\n${directive}\n`;
    }
    return config;
  });
