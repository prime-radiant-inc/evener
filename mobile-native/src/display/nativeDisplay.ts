import { Storage } from "expo-sqlite/kv-store";
import { Appearance } from "react-native";
import { colorSchemeFor, DisplayPreferences } from "./displayPreferences";

/** The phone's display choices, stored with expo-sqlite's key-value store. */
export const displayPreferences = new DisplayPreferences(Storage);

/** Applies the appearance choice now, and again on every change (spec 12).
 * App.tsx calls it once, before its first render. */
export function followAppearanceChoice(prefs: DisplayPreferences = displayPreferences): () => void {
	let applied = prefs.getSnapshot().appearance;
	Appearance.setColorScheme(colorSchemeFor(applied));
	return prefs.subscribe(() => {
		const next = prefs.getSnapshot().appearance;
		if (next === applied) return;
		applied = next;
		Appearance.setColorScheme(colorSchemeFor(next));
	});
}
