import type { NativePreferencesSnapshot } from "./nativePreferences";

/** Whether editing or saving the transcript display draft should be
 * disabled. A restored draft renders synchronously from the port, before
 * the hub's first read ever lands, so the general `disabled` reasons -
 * loading/saving/writeUncertain/storageUnavailable/disconnected - are not
 * enough on their own: `confirmed` stays null through that whole window,
 * and every edit and save composes against the confirmed value (the
 * connected store's assertEditable throws until `loaded`). Local discard
 * needs none of this - it is gated separately and stays enabled with no
 * confirmed value at all (#1693); this function is never used for it.
 *
 * Kept out of TranscriptPreferencesEditor.tsx (which pulls in react-native)
 * so it can be pinned without a JSX render harness (#1526). */
export function transcriptEditingDisabled(
	state: NativePreferencesSnapshot["transcriptMobile"],
	connected: boolean,
): boolean {
	return (
		!connected ||
		state.loading ||
		state.saving ||
		state.writeUncertain ||
		state.storageUnavailable ||
		state.confirmed === null
	);
}
