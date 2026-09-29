import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { holdSubagentTree, type SubagentTree, type SubagentTreeSnapshot, subagentTree } from "./subagentTree";

/** A coordinator's shared tree, held while the caller is mounted and reading
 * through `client`, or not reading while it's null. It takes the client
 * rather than the connection, so a transcript row can hold the tree without
 * the connection's modules. */
export function useHeldSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
	client: ConversationClientLike | null,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } {
	const tree = useMemo(() => subagentTree(hubId, ref, threadId), [hubId, ref, threadId]);
	useEffect(() => holdSubagentTree(tree), [tree]);
	useEffect(() => {
		void tree.setClient(client);
	}, [tree, client]);
	const snapshot = useSyncExternalStore(tree.subscribe, tree.getSnapshot);
	return { tree, snapshot };
}
