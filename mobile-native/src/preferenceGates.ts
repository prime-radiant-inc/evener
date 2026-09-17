import type { PreferenceState } from "./nativePreferences";

/** A section's general disabled reasons - loading, saving, an unconfirmed
 * write, a storage failure, or no connection - shared by both preference
 * sections (transcript display, keybindings), which is why this takes the
 * section-agnostic `PreferenceState<unknown>` rather than either section's
 * own confirmed-value type. None of these is about whether the hub has
 * confirmed anything yet: a screen that also needs THAT (editing/saving, but
 * never local discard) composes it with `editingDisabled` below. */
export function disabled(state: PreferenceState<unknown>, connected: boolean): boolean {
	return !connected || state.loading || state.saving || state.writeUncertain || state.storageUnavailable;
}

/** Whether editing or saving a section's draft should be disabled. A
 * restored draft renders synchronously from the port, before the hub's
 * first read ever lands, so `disabled` above is not enough on its own:
 * `confirmed` stays null through that whole window, and every edit and save
 * composes against the confirmed value (the connected store's
 * assertEditable throws until `loaded`). Local discard needs none of this -
 * it is gated on `disabled` alone and stays enabled with no confirmed value
 * at all (#1693); this function is never used for it.
 *
 * Kept out of the two screens' .tsx files (which pull in react-native) so it
 * can be pinned without a JSX render harness (#1526). */
export function editingDisabled(state: PreferenceState<unknown>, connected: boolean): boolean {
	return disabled(state, connected) || state.confirmed === null;
}
