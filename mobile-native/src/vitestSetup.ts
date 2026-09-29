// Runs before every mobile-native test file (vitest.config.mts setupFiles):
// fakes for native modules vitest can't load, which a suite would otherwise
// have to mock only because a screen it renders reaches them. A suite's own
// vi.mock of the same module replaces these.
//
// - expo-haptics: haptics.ts plays it from screens across the app. The fake
//   records what played in the testkit's playedHaptics, so haptics.ts runs
//   for real and a suite can assert on what a press played.
// - expo-sqlite/kv-store: the device's key-value store, which the Haptics
//   switch (Hub > In-app alerts) is read from. In memory, per test file.
// - expo-glass-effect: the system Liquid Glass the bottom bars wear
//   (design/BarFrame). GlassView is an inert host element, and whether the
//   API is there reads the testkit's systemGlass (off unless a test says).
// - expo-symbols: the SF Symbols every grouped row draws its glyph with
//   (sheet/Grouped.tsx), which MarketplaceBrowser renders. An inert host
//   element, as the suites that mock it themselves draw it.
import { vi } from "vitest";

// react-test-renderer logs a deprecation warning through console.error on
// every create() unless it is told it runs in a React Native test
// environment; the flag also selects the synchronous root React Native's
// testing environment uses instead of a concurrent one. Suites that render
// through renderNative.testkit.tsx would otherwise bury real stderr under one
// line per render (#2433).
(globalThis as { IS_REACT_NATIVE_TEST_ENVIRONMENT?: boolean }).IS_REACT_NATIVE_TEST_ENVIRONMENT = true;

vi.mock("expo-haptics", async () => {
	const { playedHaptics } = await import("./renderNative.testkit");
	return {
		ImpactFeedbackStyle: { Light: "light", Rigid: "rigid" },
		NotificationFeedbackType: { Success: "success", Warning: "warning" },
		selectionAsync: async () => void playedHaptics.push("selection"),
		impactAsync: async (style: string) => void playedHaptics.push(`impact:${style}`),
		notificationAsync: async (type: string) => void playedHaptics.push(`notification:${type}`),
	};
});

vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

vi.mock("expo-glass-effect", async () => {
	const { systemGlass } = await import("./renderNative.testkit");
	return {
		GlassView: "GlassView",
		isGlassEffectAPIAvailable: () => {
			if (systemGlass.available === "throws") throw new Error("Cannot find native module 'ExpoGlassEffect'");
			return systemGlass.available;
		},
	};
});

vi.mock("expo-sqlite/kv-store", () => {
	const values = new Map<string, string>();
	return {
		Storage: {
			getItemSync: (key: string) => values.get(key) ?? null,
			setItemSync: (key: string, value: string) => void values.set(key, value),
			removeItemSync: (key: string) => void values.delete(key),
		},
	};
});
