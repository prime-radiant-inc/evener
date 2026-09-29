// Whether the system's Liquid Glass (spec 16.3) draws the app's bars: the
// device has it (iOS 26 and later), and Reduce Transparency is known to be off.
import { isGlassEffectAPIAvailable } from "expo-glass-effect";
import { useReduceTransparency } from "../accessibilitySettings";

/** Whether the device has the Liquid Glass API. expo-glass-effect reads its
 * native module once per process and checks the API itself (iOS 26 betas
 * lacked it); reading the module throws only for a binary built without it,
 * which gets the opaque fill. */
export function systemGlassAvailable(): boolean {
	try {
		return isGlassEffectAPIAvailable();
	} catch {
		return false;
	}
}

/** Whether a bar wears the glass now. It waits for Reduce Transparency to be
 * known to be off, so a reader with it on never sees a flash of glass. */
export function useSystemGlass(): boolean {
	const reduceTransparency = useReduceTransparency();
	return systemGlassAvailable() && reduceTransparency === false;
}

/** A stack screen's nav bar options for `glass`: transparent over the
 * screen, which draws its own glass under the bar and the rows beneath it,
 * or opaque on the page color `page` the stack's bar otherwise wears. */
export function navBarGlassOptions(glass: boolean, page: string) {
	return {
		headerTransparent: glass,
		// react-native-screens draws a transparent bar clear only when its
		// background color is itself clear.
		headerStyle: { backgroundColor: glass ? "transparent" : page },
		// The screen's glass is the bar's edge; the system's own edge effect
		// would draw a second one over it.
		scrollEdgeEffects: { top: glass ? ("hidden" as const) : ("automatic" as const) },
	};
}
