import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/** An on/off accessibility setting, read at mount and followed as it changes
 * (reanimated's useReducedMotion reads Reduce Motion once, at launch). */
function useAccessibilitySetting(
	read: () => Promise<boolean>,
	event: "reduceMotionChanged" | "reduceTransparencyChanged",
): boolean {
	const [on, setOn] = useState(false);
	useEffect(() => {
		let live = true;
		// A change that arrives before the mount-time read answers is newer
		// than that read, so the read must not overwrite it.
		let changed = false;
		void read().then((value) => {
			if (live && !changed) setOn(value);
		});
		const subscription = AccessibilityInfo.addEventListener(event, (value) => {
			changed = true;
			setOn(value);
		});
		return () => {
			live = false;
			subscription.remove();
		};
	}, [read, event]);
	return on;
}

// Read at call time, so a test's spy on AccessibilityInfo is the one called.
const readReduceMotion = () => AccessibilityInfo.isReduceMotionEnabled();
const readReduceTransparency = () => AccessibilityInfo.isReduceTransparencyEnabled();

/** Whether Reduce Motion is on, following the setting as it changes. */
export function useReduceMotion(): boolean {
	return useAccessibilitySetting(readReduceMotion, "reduceMotionChanged");
}

/** Whether Reduce Transparency is on (iOS), following the setting as it
 * changes: system glass gives way to an opaque fill while it is. */
export function useReduceTransparency(): boolean {
	return useAccessibilitySetting(readReduceTransparency, "reduceTransparencyChanged");
}
