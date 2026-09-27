// What a sheet does when something tries to close it (spec 6: sheets with
// unsaved input ask before discarding). A swipe down, Cancel and Android's
// back each reach the sheet's guard as a removal of its route.

export type SheetLeave = "leave" | "ask";

/** Unsaved input asks first, unless the sheet is finishing on purpose: its
 * Send or Add went through, or it is leading somewhere else. */
export function sheetLeave(dirty: boolean, finishing: boolean): SheetLeave {
	return dirty && !finishing ? "ask" : "leave";
}

export interface SheetAlertButton {
	text: string;
	style: "cancel" | "destructive";
	onPress?: () => void;
}

/** The question a sheet with unsaved input asks before it goes: stay, or
 * throw the input away. Keeping is the safe answer, so it comes first. */
export function discardAlert(title: string, discard: () => void): { title: string; buttons: SheetAlertButton[] } {
	return {
		title,
		buttons: [
			{ text: "Keep editing", style: "cancel" },
			{ text: "Discard", style: "destructive", onPress: discard },
		],
	};
}

/** The question when a sheet names nothing more specific to lose. */
export const DISCARD_TITLE = "Discard your changes?";
