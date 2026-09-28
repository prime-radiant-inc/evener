// A Board controller's own retry for its failed reads, shared by the Board
// and the Session's read of the fleet (useFleet).
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { reconnectDelay } from "../hubConnection";
import type { BoardController, BoardSnapshot } from "./boardData";

/** While any of the Board's reads has failed on a ready connection (Live,
 * Needs you, the pin catalog, a category or the manifest; first read or
 * later), rebind the client after a backoff that grows with each failed
 * attempt. Nothing else would retry it while the Board stays in view: an
 * idle fleet sends no invalidations, and the controller retries failed
 * reads only when it resumes, on a focus change. The retry waits while
 * another read is still out, so a rebind never cancels a healthy read.
 * Rebinding is the reconnect path: the loaded rows stay on screen until the
 * fresh reads land, and the screen's load-more pages Live back out. A read
 * that lands, or a new connection, starts the count over; with no client
 * (disconnected or out of view) the hook holds its count and schedules
 * nothing. */
export function useBoardReadRetry(
	board: BoardController,
	client: ConversationClientLike | null,
	snapshot: Pick<BoardSnapshot, "loaded" | "retained" | "reading" | "error">,
) {
	const [retries, setRetries] = useState({ client, count: 0 });
	const count = retries.client === client ? retries.count : 0;
	const failed = snapshot.error !== null && !snapshot.reading;
	// Only fresh reads that settled count as success: the Board has loaded
	// and shows nothing retained, with no error and no read in flight. A read
	// a pause cancelled leaves no error and no loading flag, but it never
	// lands, so its page stays unloaded or retained.
	const succeeded = snapshot.loaded && !snapshot.retained && snapshot.error === null && !snapshot.reading;
	useEffect(() => {
		if (client && succeeded && count > 0) setRetries({ client, count: 0 });
	}, [client, count, succeeded]);
	useEffect(() => {
		if (!failed || !client) return;
		const timer = setTimeout(() => {
			board.setClient(client);
			setRetries({ client, count: count + 1 });
		}, reconnectDelay(count + 1));
		return () => clearTimeout(timer);
	}, [board, client, failed, count]);
}
