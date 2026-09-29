// How a navigation header's text follows Dynamic Type: its buttons and its
// title grow with the text size up to xxxLarge and stop there, since the bar's
// height is fixed; a word never breaks across the header.
import { useTextScale } from "./ui";

/** Body at xxxLarge, the largest size before the accessibility sizes: 23pt
 * over the default 17 (Apple's Dynamic Type sizes). */
const XXXL_SCALE = 23 / 17;

/** How much a header's text grows: with Dynamic Type, up to xxxLarge. */
export function useHeaderTextScale(): number {
	return Math.min(useTextScale(), XXXL_SCALE);
}

/** A native stack's header title in `color`: Headline, semibold 17
 * (spec 16.2), growing as useHeaderTextScale says. */
export function useHeaderTitleStyle(color: string) {
	return { color, fontSize: 17 * useHeaderTextScale(), fontWeight: "600" as const };
}
