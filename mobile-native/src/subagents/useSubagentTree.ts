import { useConnection } from "../ConnectionProvider";
import type { SubagentTree, SubagentTreeSnapshot } from "./subagentTree";
import { useHeldSubagentTree } from "./useHeldSubagentTree";

/** A coordinator's shared tree for a screen: held while the screen is
 * mounted, and reading only while this hub is connected. */
export function useSubagentTree(
	hubId: string,
	ref: string,
	threadId: string,
): { tree: SubagentTree; snapshot: SubagentTreeSnapshot } {
	const { client, state, activeProfile } = useConnection();
	const connected = state === "ready" && activeProfile?.id === hubId;
	return useHeldSubagentTree(hubId, ref, threadId, connected ? client : null);
}
