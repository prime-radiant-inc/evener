// The session screen marks itself seen (S4), so a session opened from a
// search hit no loaded Board page lists, from a link or from Next loses its
// blue dot too.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { hubTime } from "./attention";
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
 * Each source marks a given turn end at most once while the screen stays in
 * front. So a mark the hub refuses is not sent again on every re-render, and
 * a Mark as unread made elsewhere at a turn end already marked here wins; a
 * newer turn end is marked again.
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
	const snapshotKey = lastTurnEndedAt ? `${hubId}\u0000${ref}\u0000${lastTurnEndedAt}` : undefined;
	useOncePerStayInFront(inFront, snapshotKey, () => {
		if (!lastTurnEndedAt) return;
		// The model's ISO string round-trips the hub's milliseconds exactly.
		hubSeenMarks(hubId).markSeen(client, [{ ref, seenThrough: Date.parse(lastTurnEndedAt) }]);
	});
	// The device decides a row without a readable hub turn end by its
	// updated_at, so that is what a new mark follows for such a row.
	const rowKey = fleetRow && `${hubId}\u0000${ref}\u0000${hubTime(fleetRow.turn_ended_at) ?? fleetRow.updated_at ?? ""}`;
	// Recorded even when the row already reads seen: an unread marked
	// elsewhere at this same turn end later is left alone.
	useOncePerStayInFront(inFront, rowKey, () => {
		if (fleetRow && !seen.isSeen(fleetRow)) seen.markRead(client, [fleetRow]);
	});
	useEffect(() => {
		if (client) hubSeenMarks(hubId).flush(client);
	}, [hubId, client]);
}

/** Runs `mark` once per `key` while in front, and again for the same key on
 * a new visit to the front; an undefined key marks nothing. The effect runs
 * on every new `mark`, and the key check is what keeps it to once. */
function useOncePerStayInFront(inFront: boolean, key: string | undefined, mark: () => void): void {
	// The key last marked in this stay in front, or null.
	const marked = useRef<string | null>(null);
	useEffect(() => {
		if (!inFront) marked.current = null;
		if (!inFront || key === undefined || marked.current === key) return;
		marked.current = key;
		mark();
	}, [inFront, key, mark]);
}
