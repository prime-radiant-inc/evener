import { useEffect, useState } from "react";
import { Keyboard, Platform } from "react-native";

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

/** Whether you're typing in the composer: the keyboard is up and it is the
 * composer's (`composerKeyboard`: the composer shows and nothing else, such
 * as the find bar or a dock's field, has the keyboard). While you type, what
 * crowds the transcript folds or steps aside: the queue, Next, the header's
 * chips and note, a question's options.
 *
 * Each of those calls this itself, so the keyboard coming and going
 * re-renders only them. A screen-wide render as the keyboard starts to rise
 * would re-render the transcript's cells, and Reanimated holds its own
 * commits while React commits, stalling the keyboard controller's per-frame
 * padding for as long as that takes (#3247). */
export function useComposerTyping(composerKeyboard: boolean): boolean {
	return useKeyboardShown() && composerKeyboard;
}
