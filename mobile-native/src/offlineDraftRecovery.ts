/** Whether a "Discard" action for an unreadable stored preference draft should
 * be disabled. `saving`/`writeUncertain` describe an in-flight or unresolved
 * HUB write on a live store - meaningful only while a model is connected.
 * Discarding an unreadable RECORD touches only this phone, so a stale flag
 * left over from before the connection dropped must not disable the one
 * recovery that state allows, with no way to clear it while discarding is the
 * thing being blocked. The transcript detail screen uses this gate for its own
 * draft, whose store has the same two write flags. */
export function unreadableDraftDiscardDisabled(
	connectedWithModel: boolean,
	domain: { loading?: boolean; saving?: boolean; writeUncertain?: boolean } | undefined,
): boolean {
	if (!connectedWithModel) return false;
	return !!domain?.loading || !!domain?.saving || !!domain?.writeUncertain;
}
