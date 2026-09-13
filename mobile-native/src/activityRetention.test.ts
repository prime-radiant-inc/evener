import { expect, it } from "vitest";
import { parseActivityTree } from "../../cmd/evener-hub/frontend/src/protocol/activityData";
import { ActivityList } from "../../cmd/evener-hub/frontend/src/protocol/activityList";
import { retainedActivityTree } from "./activityRetention";

function activityTree() {
	const tree = parseActivityTree({
		revision: 1,
		root: {
			kind: "session",
			sessionId: "thread-1",
			ref: "local:one",
			label: "Test",
			aggregate: "running",
			counts: { active: 0, completed: 0, failed: 0, complete: true },
			branch: {},
			entries: [],
		},
	});
	if (!tree) throw new Error("invalid test activity tree");
	return tree;
}

it("drops retained activity when its session identity changes", () => {
	const tree = activityTree();
	const retained = { ref: "local:one", threadId: "thread-1", tree };
	const client = {
		request: async () => ({ data: {} }),
		onNotification: () => () => {},
	};

	expect(retainedActivityTree(retained, "local:one", "thread-1")).toBe(tree);
	const replacementTree = retainedActivityTree(
		retained,
		"local:one",
		"thread-2",
	);
	expect(replacementTree).toBeNull();
	expect(
		() => new ActivityList(client, "local:one", "thread-2", replacementTree),
	).not.toThrow();
});
