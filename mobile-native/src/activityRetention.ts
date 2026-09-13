import type { ActivityTree } from "../../cmd/evener-hub/frontend/src/protocol/activityData";

export type RetainedActivity = {
	ref: string;
	threadId: string;
	tree: ActivityTree;
} | null;

export function retainedActivityTree(
	retained: RetainedActivity,
	ref: string,
	threadId: string,
): ActivityTree | null {
	return retained?.ref === ref && retained.threadId === threadId
		? retained.tree
		: null;
}
