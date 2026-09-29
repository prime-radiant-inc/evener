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
