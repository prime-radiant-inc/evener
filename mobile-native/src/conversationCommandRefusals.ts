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
import { createFrameworkFreeStore } from "@evener/appwire-client";

export type CommandRefusalReason = "not-sent" | "refused" | "unconfirmed";

export interface CommandRefusal {
	reason: CommandRefusalReason;
	message: string;
}

// The store every shared store in the app is built on, so the channel's
// subscribe/notify plumbing is the same one every other cross-screen channel
// uses. The map is replaced on every write, never mutated, so a view's
// getSnapshot (the entry itself, or null) is referentially stable between
// writes - including the write an adoption makes, which notifies like any
// other and lets a later publish for the same ref re-render the composer that
// already adopted one.
const store = createFrameworkFreeStore<{ refusals: ReadonlyMap<string, CommandRefusal> }>(() => ({
	refusals: new Map(),
}));

function key(hubId: string, ref: string): string {
	return JSON.stringify([hubId, ref]);
}

/** Publish a refusal for the composer that opens on this hub/ref to show. */
export function publishCommandRefusal(hubId: string, ref: string, refusal: CommandRefusal): void {
	store.setState((state) => ({
		refusals: new Map(state.refusals).set(key(hubId, ref), refusal),
	}));
}

/** The refusal waiting for this hub/ref, or null: the snapshot a mounted
 * composer reads through useSyncExternalStore. Stable between writes. */
export function commandRefusalPublished(hubId: string, ref: string): CommandRefusal | null {
	return store.getState().refusals.get(key(hubId, ref)) ?? null;
}

/** Adopt the published refusal for this hub/ref, if one is waiting: the
 * composer that opens on the ref shows it as its own action error. Taking the
 * entry notifies, so the composer's snapshot settles back to null and a later
 * refusal for the same ref still reaches it. */
export function adoptCommandRefusal(hubId: string, ref: string): CommandRefusal | null {
	const k = key(hubId, ref);
	const refusal = store.getState().refusals.get(k) ?? null;
	if (refusal !== null) {
		const refusals = new Map(store.getState().refusals);
		refusals.delete(k);
		store.setState({ refusals });
	}
	return refusal;
}

/** Drop a published refusal, as a screen does when a new action begins. */
export function clearCommandRefusal(hubId: string, ref: string): void {
	const k = key(hubId, ref);
	if (!store.getState().refusals.has(k)) return;
	const refusals = new Map(store.getState().refusals);
	refusals.delete(k);
	store.setState({ refusals });
}

/** The subscription the screens' useSyncExternalStore rides. */
export function subscribeToCommandRefusals(listener: () => void): () => void {
	return store.subscribe(listener);
}
