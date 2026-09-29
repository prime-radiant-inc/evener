import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

type SettingEvent = "reduceMotionChanged" | "reduceTransparencyChanged";

// The last value each setting was known to have, so a later mount (a
// session's bar, over the Board's) starts from it rather than from unknown.
// It is kept only while some mount follows the setting: with none, the
// setting could change unheard, so it is forgotten.
const lastKnown = new Map<SettingEvent, boolean>();
const following = new Map<SettingEvent, number>();

/** An on/off accessibility setting, read at mount and followed as it
 * changes (reanimated's useReducedMotion reads Reduce Motion once, at
 * launch). Until this mount's read or a change says, it is the value
 * another mount is following, or null; still so if the read fails. */
function useAccessibilitySetting(read: () => Promise<boolean>, event: SettingEvent): boolean | null {
	const [on, setOn] = useState<boolean | null>(() => lastKnown.get(event) ?? null);
	useEffect(() => {
		let live = true;
		// A change that arrives before the mount-time read answers is newer
		// than that read, so the read must not overwrite it.
		let changed = false;
		following.set(event, (following.get(event) ?? 0) + 1);
		read().then(
			(value) => {
				// A read that answers after its mount is gone says nothing
				// anyone is following.
				if (changed || !live) return;
				lastKnown.set(event, value);
				setOn(value);
			},
			// A read that fails leaves the setting unknown; a later change
			// still says.
			() => {},
		);
		const subscription = AccessibilityInfo.addEventListener(event, (value) => {
			changed = true;
			lastKnown.set(event, value);
			setOn(value);
		});
		return () => {
			live = false;
			subscription.remove();
			const remaining = (following.get(event) ?? 1) - 1;
			following.set(event, remaining);
			if (remaining === 0) lastKnown.delete(event);
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
