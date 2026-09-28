// The session screen marks itself seen (S4), so a session opened from a
// search hit no loaded Board page lists, from a link or from Next loses its
// blue dot too.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useEffect } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { type BoardSeen, hubSeenMarks } from "./hubSeen";

/** Marks the session seen while the screen is in front, so a turn that ends
 * while you watch goes straight to Idle (Jesse's ruling, 2026-09-29, over the
 * server plan's ruling 18). Two sources, both the hub's own stamps:
 * - the loaded snapshot's turn end (conversation is null until it loads),
 *   which marks on arriving in front and after any re-read;
 * - the fleet's row for this session, which the fleet re-reads when the hub
 *   invalidates it, as it does when a turn ends. The snapshot's turn end has
 *   no live push, so this is what catches a turn that ends while you watch.
 *   A row with no hub turn end is the device's SeenMarkers' to mark.
 * A mark made with no ready client waits in the hub's controller, and goes
 * out when this screen next has one: the Board may not be mounted to flush
 * it. */
export function useMarkSeenInFront(
	session: { hubId: string; ref: string },
	inFront: boolean,
	client: ConversationClientLike | null,
	conversation: { lastTurnEndedAt?: string } | null,
	fleetRow: NavigationSessionSummary | undefined,
	seen: BoardSeen,
): void {
	const { hubId, ref } = session;
	const lastTurnEndedAt = conversation?.lastTurnEndedAt;
	useEffect(() => {
		if (!inFront || !lastTurnEndedAt) return;
		// The model's ISO string round-trips the hub's milliseconds exactly.
		// markSeen sends nothing for a turn end it already holds.
		hubSeenMarks(hubId).markSeen(client, [{ ref, seenThrough: Date.parse(lastTurnEndedAt) }]);
	}, [hubId, ref, inFront, lastTurnEndedAt, client]);
	useEffect(() => {
		// Only an unseen row is marked, so a device mark, which changes `seen`,
		// runs this once more and stops.
		if (!inFront || !fleetRow || seen.isSeen(fleetRow)) return;
		seen.markRead(client, [fleetRow]);
	}, [inFront, fleetRow, seen, client]);
	useEffect(() => {
		if (client) hubSeenMarks(hubId).flush(client);
	}, [hubId, client]);
}
