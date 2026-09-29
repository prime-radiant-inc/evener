// A Session header row's fill (spec 16.3): clear on the header's glass, where
// the nav bar is the system's glass, and the page color elsewhere.
import type { Palette } from "../design/tokens";

export function headerRowFill(onGlass: boolean, palette: Palette): string {
	return onGlass ? "transparent" : palette.page;
}
