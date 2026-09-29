import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/** An on/off accessibility setting, read at mount and followed as it
 * changes (reanimated's useReducedMotion reads Reduce Motion once, at
 * launch). Null until the first read or change says, and still null if the
 * read fails. */
function useAccessibilitySetting(
	read: () => Promise<boolean>,
	event: "reduceMotionChanged" | "reduceTransparencyChanged",
): boolean | null {
	const [on, setOn] = useState<boolean | null>(null);
	useEffect(() => {
		let live = true;
		// A change that arrives before the mount-time read answers is newer
		// than that read, so the read must not overwrite it.
		let changed = false;
		read().then(
			(value) => {
				if (live && !changed) setOn(value);
			},
			// A read that fails leaves the setting unknown; a later change
			// still says.
			() => {},
		);
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

/** Whether Reduce Motion is on, following the setting as it changes; off
 * until the setting is known. */
export function useReduceMotion(): boolean {
	return useAccessibilitySetting(readReduceMotion, "reduceMotionChanged") ?? false;
}

/** Whether Reduce Transparency is on (iOS), following the setting as it
 * changes; null until it is known, so system glass waits for a read that
 * says it is off. */
export function useReduceTransparency(): boolean | null {
	return useAccessibilitySetting(readReduceTransparency, "reduceTransparencyChanged");
}
