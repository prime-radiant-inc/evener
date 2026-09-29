// Closing a sheet route as a person would (spec 6): a sheet with unsaved input
// asks before it goes, whether by Cancel, a swipe down or Android's back.
import { useNavigation, usePreventRemove } from "@react-navigation/native";
import { useEffect, useMemo, useRef } from "react";
import { Alert } from "react-native";
import { DISCARD_TITLE, discardAlert, sheetLeave } from "./sheetLeave";

export interface SheetController {
	/** Close as a person would: a sheet with unsaved input asks first. */
	close(): void;
	/** Leave on purpose, without asking: its Send or Add went through, or it
	 * leads somewhere else. Without `then`, the sheet goes back. With `then`,
	 * `then` removes the sheet itself: `navigation.goBack()` before a
	 * `navigate`, or `returnToSession`'s pop. A screen pushed while a sheet is
	 * still up lands under the sheet (react-native-screens pushes cards on the
	 * main stack and presents sheets over it), so the sheet always goes first. */
	finish(then?: () => void): void;
}

export interface SheetOptions {
	/** Unsaved input: closing asks "Keep editing" or "Discard". */
	dirty?: boolean;
	/** The question that alert asks, such as "Discard this comment?". */
	discardTitle?: string;
	/** Runs once when the sheet goes away, however it closed. */
	onClosed?: () => void;
}

export function useSheet({
	dirty = false,
	discardTitle = DISCARD_TITLE,
	onClosed,
}: SheetOptions = {}): SheetController {
	const navigation = useNavigation();
	const finishing = useRef(false);
	// A refused swipe down (native-stack's onNativeDismissCancelled), Cancel and
	// Android's back all arrive here while `dirty` holds the route.
	usePreventRemove(dirty, ({ data }) => {
		const leave = () => navigation.dispatch(data.action);
		if (sheetLeave(dirty, finishing.current) === "leave") {
			// Consumed: a bypass is good for the one removal it was set for, not
			// every later dismissal of this same mounted sheet.
			finishing.current = false;
			leave();
			return;
		}
		confirmDiscard(leave, discardTitle);
	});
	const closed = useRef(onClosed);
	closed.current = onClosed;
	useEffect(() => () => closed.current?.(), []);
	return useMemo(
		() => ({
			close: () => navigation.goBack(),
			finish: (then?: () => void) => {
				finishing.current = true;
				if (then) then();
				else navigation.goBack();
			},
		}),
		[navigation],
	);
}

/** Asks before unsaved input goes: "Discard your changes?" (or `title`),
 * Keep editing first, and only Discard runs `discard`. */
export function confirmDiscard(discard: () => void, title = DISCARD_TITLE): void {
	const alert = discardAlert(title, discard);
	Alert.alert(alert.title, undefined, alert.buttons);
}
