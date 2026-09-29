import { useEffect, useMemo, useSyncExternalStore } from "react";
import { useConnection } from "../ConnectionProvider";
import { holdSubagentTree, type SubagentTree, type SubagentTreeSnapshot, subagentTree } from "./subagentTree";

/** A coordinator's shared tree for a screen: held while the screen is
 * mounted, and reading only while this hub is connected. */
export function useSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } {
	const { client, state, activeProfile } = useConnection();
	const tree = useMemo(() => subagentTree(hubId, ref, threadId), [hubId, ref, threadId]);
	useEffect(() => holdSubagentTree(tree), [tree]);
	const connected = state === "ready" && activeProfile?.id === hubId;
	useEffect(() => {
		void tree.setClient(connected ? client : null);
	}, [tree, connected, client]);
	const snapshot = useSyncExternalStore(tree.subscribe, tree.getSnapshot);
	return { tree, snapshot };
}
