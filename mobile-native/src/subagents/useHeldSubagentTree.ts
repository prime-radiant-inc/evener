import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { holdSubagentTree, type SubagentTree, type SubagentTreeSnapshot, subagentTree } from "./subagentTree";

/** Which coordinator's subagent tree: its session ref and thread. */
export interface SubagentTreeTarget {
	ref: string;
	threadId: string;
}

const NOTHING_HELD = () => () => {};
const NO_SNAPSHOT = () => null;

/** A coordinator's shared tree, held while the caller is mounted and `target`
 * is set, and reading through `client`, or not reading while it's null. It
 * takes the client rather than the connection, so a caller can hold the tree
 * without the connection's modules. Null while there's no target. */
export function useHeldSubagentTree(
	hubId: string,
	target: SubagentTreeTarget,
	client: ConversationClientLike | null,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot };
export function useHeldSubagentTree(
	hubId: string,
	target: SubagentTreeTarget | null,
	client: ConversationClientLike | null,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } | null;
export function useHeldSubagentTree(
	hubId: string,
	target: SubagentTreeTarget | null,
	client: ConversationClientLike | null,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } | null {
	const ref = target?.ref;
	const threadId = target?.threadId;
	const tree = useMemo(
		() => (ref !== undefined && threadId !== undefined ? subagentTree(hubId, ref, threadId) : null),
		[hubId, ref, threadId],
	);
	useEffect(() => (tree ? holdSubagentTree(tree) : undefined), [tree]);
	useEffect(() => {
		if (tree) void tree.setClient(client);
	}, [tree, client]);
	const snapshot = useSyncExternalStore(tree?.subscribe ?? NOTHING_HELD, tree?.getSnapshot ?? NO_SNAPSHOT);
	return tree && snapshot ? { tree, snapshot } : null;
}
