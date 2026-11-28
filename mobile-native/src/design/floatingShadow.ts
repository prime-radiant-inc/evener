// The one float-family shadow recipe: every floating layer — the under-bar's
// toast and compose button, the transcript's floating stack, the Duo split's
// sidebar — rests on the same soft cast so the family restyles together
// (issue #7940). A surface passes only its offset direction and blur radius.
import type { ViewStyle } from "react-native";

/** What a floating element wears so it reads as lifted off the page: the
 * shadow* props iOS draws, and the elevation Android draws instead. */
export function floatingShadow(offset: { width: number; height: number }, radius: number): ViewStyle {
	return {
		shadowColor: "#000",
		shadowOpacity: 0.12,
		shadowRadius: radius,
		shadowOffset: offset,
		elevation: 3,
	};
}
