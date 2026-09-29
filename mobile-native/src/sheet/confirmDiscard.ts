// The one question unsaved input asks before it goes (spec 6), for a sheet
// route (useSheet), a modal sheet, or a page: "Discard your changes?", with
// Keep editing first.
import { Alert } from "react-native";
import { DISCARD_TITLE, discardAlert } from "./sheetLeave";

/** Asks "Discard your changes?" (or `title`); only Discard runs `discard`. */
export function confirmDiscard(discard: () => void, title = DISCARD_TITLE): void {
	const alert = discardAlert(title, discard);
	Alert.alert(alert.title, undefined, alert.buttons);
}
