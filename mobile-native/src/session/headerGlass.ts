// Whether a Session header row draws on the header's glass (spec 16.3): clear
// there, on the page color elsewhere. SessionHeader provides it; the rows it
// holds (the chips, the notes bar) read it.
import { createContext, useContext } from "react";
import { useColors } from "../ui";

export const OnHeaderGlass = createContext(false);

/** A header row's fill: clear on the glass, the page color off it. */
export function useHeaderRowFill(): string {
	const { palette } = useColors();
	return useContext(OnHeaderGlass) ? "transparent" : palette.page;
}
