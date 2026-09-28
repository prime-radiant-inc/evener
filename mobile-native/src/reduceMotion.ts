import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/** Whether Reduce Motion is on, following the setting as it changes
 * (reanimated's useReducedMotion reads it once, at launch). */
export function useReduceMotion(): boolean {
	const [reduce, setReduce] = useState(false);
	useEffect(() => {
		let live = true;
		// A change that arrives before the mount-time read answers is newer
		// than that read, so the read must not overwrite it.
		let changed = false;
		void AccessibilityInfo.isReduceMotionEnabled().then((value) => {
			if (live && !changed) setReduce(value);
		});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", (value) => {
			changed = true;
			setReduce(value);
		});
		return () => {
			live = false;
			subscription.remove();
		};
	}, []);
	return reduce;
}
