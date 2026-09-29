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

export type ReadingFace = { fontFamily?: string; fontWeight?: "600" };

const SERIF_FACES: Record<"regular" | "semibold", ReadingFace> = {
	regular: { fontFamily: fonts.serif },
	semibold: { fontFamily: fonts.serifSemibold },
};
const SANS_FACES: Record<"regular" | "semibold", ReadingFace> = {
	regular: {},
	semibold: { fontWeight: "600" },
};

/** The face for agent-written text outside the reading roles (a thought, a
 * question, a document's title, your comment): the serif, or under Sans the
 * system face, with a semibold weight standing in for the semibold serif. */
export function useReadingFace(): (weight: "regular" | "semibold") => ReadingFace {
	const faces = useDisplayChoices().readingFont === "sans" ? SANS_FACES : SERIF_FACES;
	return (weight) => faces[weight];
}
