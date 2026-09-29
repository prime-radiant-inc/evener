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
