// The session screen marks itself seen (S4), so a session opened from a
// search hit no loaded Board page lists, from a link or from Next loses its
// blue dot too. Imports nothing that reaches expo-sqlite/kv-store.
import { useEffect, useRef } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { hubSeenMarks } from "./hubSeen";

/** Marks the session seen through its snapshot's turn end once per visit to
 * the front: when the screen is in front and its conversation has loaded
 * (conversation is null until then). The visit counts as marked once it has
 * loaded in front, turn end or not, so a turn that ends while the screen
 * stays in front is not "opened since" (the server plan's ruling 18). A mark
 * made with no ready client waits in the hub's controller, and goes out when
 * this screen next has one: the Board may not be mounted to flush it. */
export function useMarkSeenInFront(
	session: { hubId: string; ref: string },
	inFront: boolean,
	client: ConversationClientLike | null,
	conversation: { lastTurnEndedAt?: string } | null,
): void {
	const { hubId, ref } = session;
	// The session this visit has marked, or null before it has.
	const marked = useRef<string | null>(null);
	const lastTurnEndedAt = conversation?.lastTurnEndedAt;
	const loaded = conversation !== null;
	useEffect(() => {
		if (!inFront) {
			marked.current = null;
			return;
		}
		const key = `${hubId}\u0000${ref}`;
		if (!loaded || marked.current === key) return;
		marked.current = key;
		// The model's ISO string round-trips the hub's milliseconds exactly.
		if (lastTurnEndedAt) hubSeenMarks(hubId).markSeen(client, [{ ref, seenThrough: Date.parse(lastTurnEndedAt) }]);
	}, [hubId, ref, inFront, loaded, lastTurnEndedAt, client]);
	useEffect(() => {
		if (client) hubSeenMarks(hubId).flush(client);
	}, [hubId, client]);
}
