/**
 * @type {import('@bacons/apple-targets/app.plugin').Config}
 *
 * The share extension (spec: share intent). It takes text or a web link, writes
 * it to the app group the app reads, and opens evener-native://share. It is
 * plain Swift: no JS bundle, no hub connection, no credentials.
 */
module.exports = (config) => ({
	type: "share",
	name: "EvenerShareExtension",
	displayName: "Evener",
	bundleIdentifier: ".ShareExtension",
	deploymentTarget: "16.4",
	frameworks: ["UniformTypeIdentifiers"],
	entitlements: {
		// The same app group the app writes; the container is how the two sides
		// meet, so both lists must match.
		"com.apple.security.application-groups": config.ios.entitlements["com.apple.security.application-groups"],
	},
});
