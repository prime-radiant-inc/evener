import { useEffect, useState } from "react";
import { Keyboard, Platform } from "react-native";

/** Whether the software keyboard is up. iOS says so as it starts to move,
 * so a layout that answers it moves with the keyboard; Android says so once
 * it has moved. */
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
