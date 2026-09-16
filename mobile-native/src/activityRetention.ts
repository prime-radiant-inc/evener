import type { ActivityTree } from "@evener/appwire-client";

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
