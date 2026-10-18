// The Expo config. Kept as JavaScript so the Apple team id comes from the
// environment (APPLE_TEAM_ID) instead of being committed: the share
// extension's target is generated from it by @bacons/apple-targets, which
// needs a team id at prebuild time.
//
// APPLE_TEAM_ID is required only when a target is generated (prebuild for
// iOS). Every other use of this config, including the native typecheck and
// Metro bundling, loads it without a team id.
const teamId = process.env.APPLE_TEAM_ID ?? "";

// Prebuilding iOS needs the team id: ./plugins/with-ios-preconditions refuses
// an iOS prebuild without one. Every other use of this config (typecheck,
// Metro, Android prebuild) loads without it.

const APP_GROUP = "group.com.primeradiant.evener.native";

/** @type {import("expo/config").ExpoConfig} */
const config = {
	expo: {
		name: "Evener",
		slug: "evener-native",
		version: "0.3.0",
		orientation: "portrait",
		icon: "./assets/evener-icon.png",
		userInterfaceStyle: "automatic",
		backgroundColor: "#0a0a0e",
		ios: {
			supportsTablet: false,
			bundleIdentifier: "com.primeradiant.evener.native",
			...(teamId ? { appleTeamId: teamId } : {}),
			entitlements: {
				"com.apple.security.application-groups": [APP_GROUP],
			},
			infoPlist: {
				NSLocalNetworkUsageDescription: "Connect to your Evener hubs on your local network.",
				NSAppTransportSecurity: {
					NSAllowsArbitraryLoads: true,
				},
				NSPhotoLibraryAddUsageDescription: "Save images from your Evener conversations to your photo library.",
				ITSAppUsesNonExemptEncryption: false,
			},
		},
		android: {
			adaptiveIcon: {
				backgroundColor: "#E6F4FE",
				foregroundImage: "./assets/android-icon-foreground.png",
				backgroundImage: "./assets/android-icon-background.png",
				monochromeImage: "./assets/android-icon-monochrome.png",
			},
			predictiveBackGestureEnabled: false,
			package: "com.primeradiant.evener.mobile",
		},
		web: {
			favicon: "./assets/favicon.png",
		},
		plugins: [
			"./plugins/with-ios-preconditions",
			"./plugins/with-shared-sources",
			"expo-secure-store",
			[
				"expo-build-properties",
				{
					android: {
						usesCleartextTraffic: true,
					},
				},
			],
			"expo-sqlite",
			[
				"expo-font",
				{
					fonts: [
						"./node_modules/@expo-google-fonts/source-serif-4/400Regular/SourceSerif4_400Regular.ttf",
						"./node_modules/@expo-google-fonts/source-serif-4/400Regular_Italic/SourceSerif4_400Regular_Italic.ttf",
						"./node_modules/@expo-google-fonts/source-serif-4/600SemiBold/SourceSerif4_600SemiBold.ttf",
						"./node_modules/@expo-google-fonts/source-serif-4/600SemiBold_Italic/SourceSerif4_600SemiBold_Italic.ttf",
					],
				},
			],
			[
				"expo-image-picker",
				{
					photosPermission: "Choose images to attach to your Evener conversations.",
					cameraPermission: "Scan the pairing code shown in Evener on your computer, or take photos to attach to your conversations.",
					microphonePermission: false,
				},
			],
			[
				"expo-splash-screen",
				{
					backgroundColor: "#0a0a0e",
					image: "./assets/evener-icon.png",
					imageWidth: 120,
					resizeMode: "contain",
				},
			],
			"expo-web-browser",
			[
				"expo-camera",
				{
					cameraPermission: "Scan the pairing code shown in Evener on your computer, or take photos to attach to your conversations.",
					microphonePermission: false,
					recordAudioAndroid: false,
				},
			],
			// The iOS share extension target (targets/share). It is generated
			// only when a team id is present, since the plugin refuses without
			// one and the app's own JS checks (typecheck, bundling) load this
			// config on machines that never prebuild iOS.
			...(teamId ? ["@bacons/apple-targets"] : []),
		],
		scheme: "evener-native",
	},
};

module.exports = config;
