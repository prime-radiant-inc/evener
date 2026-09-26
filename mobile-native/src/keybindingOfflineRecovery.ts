// KeybindingPreferencesScreen's own offline-recovery decisions, pulled out to
// a plain module: the screen imports react-native (Flow-syntax source), which
// vitest cannot parse when a test file imports the screen module directly -
// these pure functions are the only part of that screen worth unit testing,
// and this is what makes that possible.

/** Whether the "Discard unreadable draft" action should render. `domain` is
 * the live store's snapshot - authoritative while connected, but a cold
 * offline start never reaches it (bindNativePreferences' `ready` callback
 * never fires), and it is simply STALE the moment the connection drops: no
 * live read has happened since, so a record that becomes corrupt (or is
 * fixed) while offline is invisible to it. The independently probed
 * `offlineDraftUnreadable` (see NativePreferencesProvider) is what stays
 * current in both those cases, so it is preferred over `domain` whenever
 * this screen is not connected, rather than merely filling a gap when
 * `domain` is absent. */
export function offlineAwareDraftUnreadable(
	connected: boolean,
	domainDraftUnreadable: boolean | undefined,
	offlineDraftUnreadable: boolean,
): boolean {
	return connected ? (domainDraftUnreadable ?? offlineDraftUnreadable) : offlineDraftUnreadable;
}

/** Whether the "Discard unreadable draft" action should be disabled.
 * `domain.saving`/`domain.writeUncertain` describe an in-flight or
 * unresolved HUB write - meaningful only on the connected, live-store path
 * (`run`'s own `model.discardKeybindingsDraft()`), which this same flag
 * gates elsewhere in this screen. The store-free offline path
 * (`preferences.discardUnreadableKeybindingsDraft()`) touches no hub and
 * has no write of its own to wait on; gating it on a STALE domain flag left
 * over from before the connection dropped can disable the one recovery this
 * state allows, with no way to clear it since discarding is the thing being
 * blocked. */
export function unreadableDraftDiscardDisabled(
	connectedWithModel: boolean,
	domain: { loading?: boolean; saving?: boolean; writeUncertain?: boolean } | undefined,
): boolean {
	if (!connectedWithModel) return false;
	return !!domain?.loading || !!domain?.saving || !!domain?.writeUncertain;
}

/** The message to show for a genuine offline storage-read failure - distinct
 * from `domain?.error` (a live store's own failure message), which is
 * `undefined` in exactly the case this exists for: no model has published a
 * snapshot yet, so there is no domain to have an error at all. */
export function offlineStorageErrorMessage(offlineStorageUnavailable: boolean): string | null {
	return offlineStorageUnavailable
		? "Could not read the saved shortcut draft on this phone. Check current shortcuts to retry."
		: null;
}
