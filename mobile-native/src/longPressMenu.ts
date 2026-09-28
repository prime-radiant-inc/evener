// The touch-and-hold menu the transcript's messages and the Reader's blocks
// share (phase 3's ruling 25): an action sheet on iPhone until a context-menu
// library is chosen, and an alert on Android. VoiceOver gets the same items
// as actions on the element.
import { type AccessibilityActionEvent, ActionSheetIOS, Alert, Platform } from "react-native";

/** One item of a touch-and-hold menu, which VoiceOver also offers as an
 * action on the element. */
export interface MenuItem {
	name: string;
	label: string;
	run: () => void;
}

/** Shows the menu. `onClose` runs once it goes away, whether an item was
 * chosen or not, before the chosen item runs. */
export function showMenu(items: readonly MenuItem[], preview: string, onClose?: () => void) {
	if (Platform.OS === "ios") {
		ActionSheetIOS.showActionSheetWithOptions(
			{
				options: [...items.map((item) => item.label), "Cancel"],
				cancelButtonIndex: items.length,
			},
			(index) => {
				onClose?.();
				items[index]?.run();
			},
		);
		return;
	}
	// Android's alert holds at most three buttons, so it dismisses by a tap
	// outside rather than spending one on Cancel.
	Alert.alert(
		"Message",
		preview,
		items.map((item) => ({
			text: item.label,
			onPress: () => {
				onClose?.();
				item.run();
			},
		})),
		{ cancelable: true, onDismiss: onClose },
	);
}

/** A text's first 120 characters on one line, for the Android menu. */
export function menuPreview(text: string): string {
	return text.replace(/\s+/g, " ").trim().slice(0, 120);
}

export function menuAccessibility(items: readonly MenuItem[]) {
	return {
		accessibilityActions: items.map(({ name, label }) => ({ name, label })),
		onAccessibilityAction: (event: AccessibilityActionEvent) =>
			items.find((item) => item.name === event.nativeEvent.actionName)?.run(),
	};
}
