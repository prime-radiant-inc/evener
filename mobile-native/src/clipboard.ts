import * as Clipboard from "expo-clipboard";
import { AccessibilityInfo, Alert } from "react-native";

/** Puts text on the clipboard and tells VoiceOver it's there. */
export async function copyText(text: string) {
	try {
		await Clipboard.setStringAsync(text);
		AccessibilityInfo.announceForAccessibility("Copied");
	} catch {
		Alert.alert("Could not copy", "Select the text and try copying again.");
	}
}
