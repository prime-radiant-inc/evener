// A typed command's refusal, published where the composer that must show it
// will find it even if the initiating composer has unmounted.
//
// A resume behind a command can resolve a new identity, and the screen follows
// it with a KEYED route replacement (screens.tsx's replaceForIdentity):
// react-navigation unmounts the initiating ConversationScreen and mounts a new
// one for the replacement route. A refusal written to the initiating
// component's state dies with it. This channel is keyed the same way the draft
// repository is - one entry per (hub, ref) - so the initiating screen can
// publish the refusal to the ref the surviving route points at, and the
// replacement screen adopts it on mount.
//
// The draft's own association never moves: it stays on the ref the command was
// typed on (DraftLibrary hands off only when asked), so a refusal carried here
// is the only thing that follows the identity.
export interface CommandRefusal {
	reason: "not-sent" | "refused" | "unconfirmed";
	message: string;
}

const entries = new Map<string, CommandRefusal>();
const listeners = new Set<() => void>();

function key(hubId: string, ref: string): string {
	return JSON.stringify([hubId, ref]);
}

/** Publish a refusal for the composer that opens on this hub/ref to show. */
export function publishCommandRefusal(hubId: string, ref: string, refusal: CommandRefusal): void {
	entries.set(key(hubId, ref), refusal);
	for (const listener of [...listeners]) listener();
}

/** Take the published refusal for this hub/ref, if one is waiting. The next
 * reader does not see it again. */
function take(hubId: string, ref: string): CommandRefusal | null {
	const refusal = entries.get(key(hubId, ref));
	if (refusal !== undefined) entries.delete(key(hubId, ref));
	return refusal ?? null;
}

/** Whether a refusal is waiting for this hub/ref. */
function waiting(hubId: string, ref: string): boolean {
	return entries.has(key(hubId, ref));
}

/** Drop a published refusal, as a screen does when a new action begins or the
 * person dismisses it. */
export function clearCommandRefusal(hubId: string, ref: string): void {
	if (entries.delete(key(hubId, ref))) for (const listener of [...listeners]) listener();
}

// The subscription the screens' useSyncExternalStore rides: a listener set the
// waiting readers re-render on, kept tiny because the interesting state is
// read imperatively at adoption time.
export function subscribeToCommandRefusals(listener: () => void): () => void {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
}

/** Read the channel the way useSyncExternalStore does: whether a refusal is
 * waiting for this hub/ref. */
export function commandRefusalWaiting(hubId: string, ref: string): boolean {
	return waiting(hubId, ref);
}

/** Adopt the published refusal for this hub/ref, if one is waiting: the
 * composer that opens on the ref shows it as its own action error. */
export function adoptCommandRefusal(hubId: string, ref: string): CommandRefusal | null {
	return take(hubId, ref);
}
