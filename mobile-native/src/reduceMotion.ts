import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/** Whether Reduce Motion is on, following the setting as it changes
 * (reanimated's useReducedMotion reads it once, at launch). */
export function useReduceMotion(): boolean {
	const [reduce, setReduce] = useState(false);
	useEffect(() => {
		let live = true;
		void AccessibilityInfo.isReduceMotionEnabled().then((value) => {
			if (live) setReduce(value);
		});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", setReduce);
		return () => {
			live = false;
			subscription.remove();
		};
	}, []);
	return reduce;
}
