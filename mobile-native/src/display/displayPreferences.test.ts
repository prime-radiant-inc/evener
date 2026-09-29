import { expect, it } from "vitest";
import { colorSchemeFor, DEFAULT_DISPLAY, DISPLAY_KEY, DisplayPreferences } from "./displayPreferences";

function memory(stored?: string) {
	const values = new Map<string, string>();
	if (stored !== undefined) values.set(DISPLAY_KEY, stored);
	return {
		values,
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
	};
}

it("starts at the system appearance and the serif", () => {
	expect(new DisplayPreferences(memory()).getSnapshot()).toEqual({ appearance: "system", readingFont: "serif" });
});

it("keeps a choice across launches", () => {
	const storage = memory();
	new DisplayPreferences(storage).set({ appearance: "dark" });
	expect(new DisplayPreferences(storage).getSnapshot()).toEqual({ appearance: "dark", readingFont: "serif" });
});

it("reads an unknown or broken stored value as the default, one field at a time", () => {
	expect(new DisplayPreferences(memory('{"appearance":"sepia","readingFont":"sans"}')).getSnapshot()).toEqual({
		appearance: "system",
		readingFont: "sans",
	});
	expect(new DisplayPreferences(memory("{not json")).getSnapshot()).toEqual(DEFAULT_DISPLAY);
});

it("tells its listeners once per real change", () => {
	const prefs = new DisplayPreferences(memory());
	let heard = 0;
	prefs.subscribe(() => {
		heard += 1;
	});
	prefs.set({ readingFont: "sans" });
	prefs.set({ readingFont: "sans" });
	expect(heard).toBe(1);
});

it("stores a choice again when the same choice is made after its store failed", () => {
	const values = new Map<string, string>();
	let full = true;
	const prefs = new DisplayPreferences({
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			if (full) throw new Error("disk full");
			values.set(key, value);
		},
	});
	expect(() => prefs.set({ appearance: "dark" })).toThrow("disk full");
	full = false;
	prefs.set({ appearance: "dark" });
	expect(JSON.parse(values.get(DISPLAY_KEY) ?? "{}")).toMatchObject({ appearance: "dark" });
});

it("maps each appearance to React Native's override", () => {
	expect((["system", "light", "dark"] as const).map(colorSchemeFor)).toEqual(["unspecified", "light", "dark"]);
});
