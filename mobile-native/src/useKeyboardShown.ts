import { useEffect, useState, useSyncExternalStore } from "react";
import { Keyboard, Platform } from "react-native";
import type { ComposerFocus } from "./session/composerFocus";

/** Whether the software keyboard is up. iOS says so as the keyboard starts
 * to move, so a layout that answers it changes in that first frame, while
 * the Session's avoiding view (react-native-keyboard-controller) moves with
 * the keyboard frame by frame. Android says so once the keyboard has moved. */
export function useKeyboardShown(): boolean {
	// A screen that mounts with the keyboard already up starts from that.
	const [shown, setShown] = useState(() => Keyboard.isVisible());
	useEffect(() => {
		const ios = Platform.OS === "ios";
		const subscriptions = [
			Keyboard.addListener(ios ? "keyboardWillShow" : "keyboardDidShow", () => setShown(true)),
			Keyboard.addListener(ios ? "keyboardWillHide" : "keyboardDidHide", () => setShown(false)),
		];
		return () => {
			for (const subscription of subscriptions) subscription.remove();
		};
	}, []);
	return shown;
}

/** Whether you're typing in the composer: the keyboard is up and the
 * composer's field has focus (`focus`, which the Composer reports), whatever
 * else is open, such as the find bar. While you type, what crowds the
 * transcript folds or steps aside: the queue, Next, the header's chips and
 * note, a question's options.
 *
 * Each of those calls this itself, so the keyboard and the focus coming and
 * going re-render only them. A screen-wide render as the keyboard starts to
 * rise would re-render the transcript's cells, and Reanimated holds its own
 * commits while React commits, stalling the keyboard controller's per-frame
 * padding for as long as that takes (#3247). */
export function useComposerTyping(focus: ComposerFocus): boolean {
	const focused = useSyncExternalStore(focus.subscribe, focus.getSnapshot);
	return useKeyboardShown() && focused;
}
