// The phone's display choices for components. The app's root provides the
// stored choices; a tree without a provider (a test) reads the defaults, so
// ui.tsx never has to reach native storage.
import { createContext, useContext, useSyncExternalStore } from "react";
import { fonts, typeRoles } from "../design/tokens";
import { DEFAULT_DISPLAY, type DisplayChoices, type DisplayPreferences, type ReadingFont } from "./displayPreferences";

const DisplayContext = createContext<DisplayPreferences | null>(null);
export const DisplayProvider = DisplayContext.Provider;

const noSubscription = () => () => {};
const defaultChoices = () => DEFAULT_DISPLAY;

/** The provided preferences, for a page that changes them. */
export function useDisplayPreferences(): DisplayPreferences | null {
	return useContext(DisplayContext);
}

export function useDisplayChoices(): DisplayChoices {
	const prefs = useContext(DisplayContext);
	return useSyncExternalStore(prefs?.subscribe ?? noSubscription, prefs?.getSnapshot ?? defaultChoices);
}

export type ReadingRole = { fontFamily?: string; fontSize: number; lineHeight: number };
export type ReadingRoles = Record<keyof typeof typeRoles, ReadingRole>;

function buildReadingRoles(font: ReadingFont): ReadingRoles {
	const roles = {} as ReadingRoles;
	for (const name of Object.keys(typeRoles) as (keyof typeof typeRoles)[]) {
		const role = typeRoles[name];
		roles[name] =
			font === "sans" && role.fontFamily !== fonts.mono
				? { fontSize: role.fontSize, lineHeight: role.lineHeight }
				: role;
	}
	return roles;
}

// Built once per font, so a component can key a memo on the roles it reads.
const READING_ROLES: Record<ReadingFont, ReadingRoles> = {
	serif: buildReadingRoles("serif"),
	sans: buildReadingRoles("sans"),
};

/** The reading roles for a reading font (spec 12: "Reading font: Sans" swaps
 * the serif for SF Pro). Sans keeps each role's size and line height and leaves
 * the family unset, which is the system face; machine text stays Menlo. */
export function readingRoles(font: ReadingFont): ReadingRoles {
	return READING_ROLES[font];
}

export function useReadingType(): ReadingRoles {
	return readingRoles(useDisplayChoices().readingFont);
}
