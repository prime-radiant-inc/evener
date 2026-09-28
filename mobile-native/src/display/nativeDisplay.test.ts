import { expect, it, vi } from "vitest";
import { DisplayPreferences } from "./displayPreferences";

const setColorScheme = vi.fn();
vi.mock("react-native", () => ({ Appearance: { setColorScheme: (scheme: string) => setColorScheme(scheme) } }));
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: { getItemSync: () => null, setItemSync: () => {} },
}));

it("applies the appearance at once, then only when it changes", async () => {
	const { followAppearanceChoice } = await import("./nativeDisplay");
	const values = new Map<string, string>();
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	});
	followAppearanceChoice(prefs);
	expect(setColorScheme.mock.calls).toEqual([["unspecified"]]);
	prefs.set({ readingFont: "sans" });
	prefs.set({ appearance: "dark" });
	expect(setColorScheme.mock.calls).toEqual([["unspecified"], ["dark"]]);
});
