import { useFocusEffect } from "@react-navigation/native";
import { useCallback } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { useConnection } from "../ConnectionProvider";
import { liveClientFor } from "../liveClient";
import type { SubagentTree, SubagentTreeSnapshot } from "./subagentTree";
import { useHeldSubagentTree } from "./useHeldSubagentTree";

/** A coordinator's shared tree for a screen: held while the screen is
 * mounted, and reading only while this hub is connected. */
export function useSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } {
	return useHeldSubagentTree(hubId, { ref, threadId }, liveClientFor(useConnection(), hubId));
}

/** A coordinator's shared tree for a screen that shows it live, as the
 * Activity list and a shell job's detail do: held like useSubagentTree's,
 * and followed each time the screen comes into focus, so its tree
 * notifications keep coming (ruling 9). A new client (a reconnect, or a hub
 * switch that never leaves ready) has no subscription, so the screen in
 * front follows again on each one; the held tree has already been handed
 * that client by then. Returns the live client too, for the screen's own
 * reads. */
export function useFollowedSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot; client: ConversationClientLike | null } {
	const client = liveClientFor(useConnection(), hubId);
	const { tree, snapshot } = useHeldSubagentTree(hubId, { ref, threadId }, client);
	useFocusEffect(
		useCallback(() => {
			if (client) void tree.follow();
		}, [tree, client]),
	);
	return { tree, snapshot, client };
}
