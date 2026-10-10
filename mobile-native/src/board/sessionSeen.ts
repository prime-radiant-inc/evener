// The session screen marks itself seen (S4), so a session opened from a
// search hit no loaded Board page lists, from a link or from Next loses its
// blue dot too.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { hubTime } from "./attention";
import { type BoardSeen, hubSeenMarks, tracksSeenThrough } from "./hubSeen";

/** Marks the session seen while the screen is in front, so a turn that ends
 * while you watch goes straight to Idle (Jesse's ruling, 2026-09-29, over the
 * server plan's ruling 18). Two sources, both the hub's own stamps:
 * - the loaded snapshot's turn end (conversation is null until it loads),
 *   which marks on arriving in front and after any re-read;
 * - the fleet's row for this session, which the fleet re-reads when the hub
 *   invalidates it, as it does when a turn ends. The snapshot's turn end has
 *   no live push, so this is what catches a turn that ends while you watch.
 *   A row with no hub turn end is the device's SeenMarkers' to mark.
 * A given turn end is marked at most once while the screen stays in front,
 * whichever source sees it first, so a mark the hub refuses is not sent
 * again on every re-render; a newer turn end is marked again.
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
	lastMovedAt?: number,
): void {
	const { hubId, ref } = session;
	// The turn ends marked in this stay in front, shared by the snapshot and
	// the fleet row so one turn end is marked once whichever source sees it
	// first. Keys hold the turn end in milliseconds, the hub's precision.
	const marked = useRef(new Set<string>());
	const turnKey = (ms: number) => `${hubId}\u0000${ref}\u0000${ms}`;
	const lastTurnEndedAt = conversation?.lastTurnEndedAt;
	const snapshotEnd = hubTime(lastTurnEndedAt);
	useEffect(() => {
		if (!inFront) {
			marked.current.clear();
			return;
		}
		if (snapshotEnd === null || marked.current.has(turnKey(snapshotEnd))) return;
		marked.current.add(turnKey(snapshotEnd));
		hubSeenMarks(hubId).markSeen(client, [{ ref, seenThrough: snapshotEnd }]);
	});
	// A row without a readable hub turn end is the device's to decide, by its
	// updated_at, so that is what a new mark follows for such a row.
	const rowEnd = fleetRow ? hubTime(fleetRow.turn_ended_at) : null;
	const rowKey =
		fleetRow && (rowEnd !== null ? turnKey(rowEnd) : `${hubId}\u0000${ref}\u0000updated ${fleetRow.updated_at ?? ""}`);
	// Recorded even when the row already reads seen: an unread marked
	// elsewhere at this same turn end later is left alone.
	useEffect(() => {
		if (!inFront || rowKey === undefined || marked.current.has(rowKey)) return;
		marked.current.add(rowKey);
		if (fleetRow && !seen.isSeen(fleetRow)) seen.markRead(client, [fleetRow]);
	});
	// Output streaming while you watch is seen too. The newest last motion is
	// kept while the screen is in front and marked once, when it leaves the
	// front: a mark on every activity read would rebuild the hub's navigation
	// every ten seconds while a session works. Only a hub that tracks
	// seen-through marks (the row carries seen_through) takes one, and only
	// motion after its mark is new.
	const newestMotion = useRef<number | null>(null);
	// The mark on leaving goes through the newest client, not the one that was
	// current when the motion was read.
	const latestClient = useRef(client);
	latestClient.current = client;
	const seenMark = fleetRow && tracksSeenThrough(fleetRow) ? (hubTime(fleetRow.seen_through) ?? 0) : null;
	useEffect(() => {
		if (!inFront || lastMovedAt === undefined || seenMark === null) return;
		if (lastMovedAt > (newestMotion.current ?? seenMark)) newestMotion.current = lastMovedAt;
	}, [inFront, lastMovedAt, seenMark]);
	useEffect(() => {
		if (!inFront) return;
		return () => {
			const through = newestMotion.current;
			newestMotion.current = null;
			if (through !== null) hubSeenMarks(hubId).markSeen(latestClient.current, [{ ref, seenThrough: through }]);
		};
	}, [inFront, hubId, ref]);
	useEffect(() => {
		if (client) hubSeenMarks(hubId).flush(client);
	}, [hubId, client]);
}
