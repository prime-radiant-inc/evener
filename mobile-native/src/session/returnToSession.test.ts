import { expect, it, vi } from "vitest";
import { popsToSession, returnToSession } from "./returnToSession";

const stack = {
	index: 3,
	routes: [
		{ name: "Sessions" },
		{ name: "Conversation", params: { hubId: "hub-1", ref: "local:coord", title: "Coordinator" } },
		{ name: "Subagents", params: { hubId: "hub-1", ref: "local:coord", threadId: "coord", title: "Coordinator" } },
		{ name: "Subagent", params: { hubId: "hub-1", ref: "local:fix", title: "Fix race" } },
	],
};

it("counts the pops back to a session under the current screen", () => {
	expect(popsToSession(stack, "local:coord")).toBe(2);
	expect(popsToSession(stack, "local:other")).toBeNull();
	expect(popsToSession({ index: 1, routes: stack.routes.slice(0, 2) }, "local:coord")).toBeNull();
});

it("goes back to a subagent's own screen under a document", () => {
	const reading = {
		index: 3,
		routes: [
			...stack.routes.slice(0, 2),
			{ name: "Subagent", params: { hubId: "hub-1", ref: "local:fix", title: "Fix race" } },
			{
				name: "Reader",
				params: {
					hubId: "hub-1",
					sessionRef: "local:fix",
					path: "plan.md",
					sessionTitle: "Fix race",
				},
			},
		],
	};
	expect(popsToSession(reading, "local:fix")).toBe(1);
	expect(popsToSession(reading, "local:coord")).toBe(2);
});

it("goes back to the session when it's under this screen, and opens it otherwise", () => {
	const navigation = { getState: () => stack, pop: vi.fn(), navigate: vi.fn() };
	returnToSession(navigation, { hubId: "hub-1", ref: "local:coord", title: "Coordinator" });
	expect(navigation.pop).toHaveBeenCalledWith(2);
	returnToSession(navigation, { hubId: "hub-1", ref: "local:other", title: "Other" });
	expect(navigation.navigate).toHaveBeenCalledWith("Conversation", {
		hubId: "hub-1",
		ref: "local:other",
		title: "Other",
	});
});
