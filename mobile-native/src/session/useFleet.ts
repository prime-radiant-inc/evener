// The fleet as the Session reads it (ruling 33): a Board controller of its
// own, bound to the hub's client and reading only while the Session is in
// front, classified by the Board's one seen function (BoardSeen.isSeen), so
// Back's count and Next agree with the Board.
import type { NavigationManifest } from "@evener/appwire-client";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { type LiveBands, liveBands } from "../board/attention";
import { createBoardController } from "../board/boardData";
import type { BoardSeen } from "../board/hubSeen";
import { useBoardSeen } from "../board/nativeBoardMemory";

export interface Fleet {
	bands: LiveBands;
	seen: BoardSeen;
	/** The hosts the manifest names, once it has loaded. */
	sources: NavigationManifest["sources"] | undefined;
}

/** `client` is a ready client for this hub, or null while there is none. */
export function useFleet(hubId: string, client: ConversationClientLike | null, inFront: boolean): Fleet {
	const [board] = useState(createBoardController);
	useEffect(() => () => board.dispose(), [board]);
	// Declared before the client binding, so a screen that binds its client
	// behind another screen binds a paused controller that reads nothing.
	useEffect(() => {
		if (inFront) board.resume();
		else board.pause();
	}, [board, inFront]);
	useEffect(() => {
		board.setClient(client);
	}, [board, client]);
	const snapshot = useSyncExternalStore(board.subscribe, board.getSnapshot);
	const seen = useBoardSeen(hubId);
	const bands = useMemo(
		// Never the bare seen.isSeen: it reads `this`.
		() => liveBands(snapshot.live.rows, snapshot.needsYou.rows, (row) => seen.isSeen(row)),
		[snapshot.live.rows, snapshot.needsYou.rows, seen],
	);
	return { bands, seen, sources: snapshot.manifest?.sources };
}
