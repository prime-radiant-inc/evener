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

/** Runs `leave` if the input may go now: never while its write is in flight
 * (`busy`, as a disabled Cancel does), after asking when there is input to
 * lose (`dirty`), and otherwise at once. */
export function guardLeave({ busy, dirty }: { busy: boolean; dirty: boolean }, leave: () => void): void {
	if (busy) return;
	if (dirty) confirmDiscard(leave);
	else leave();
}
