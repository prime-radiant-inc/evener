import { expect, test } from "vitest";
import type { SessionActivityContext, SessionDelegate } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { SubagentTree } from "./subagentTree";
const context: SessionActivityContext = {
	ref: "remote:root",
	sessionId: "root",
	rootRef: "remote:root",
	ancestors: [],
	ancestryKnown: true,
	epoch: "one",
	availability: "retained",
};
const delegate = (id: string, ownerRef = "remote:root"): SessionDelegate => ({
	delegateId: id,
	ownerRef,
	rootRef: context.rootRef,
	childRef: `remote:${id}`,
	type: "delegate",
	description: id,
	lifecycle: "running",
	phase: "running",
	status: "running",
	terminal: false,
	resumable: false,
});
function hub() {
	const client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread: { id: "root", modelProvider: "scripted" } }) as never);
	client.on("thread/unsubscribe", () => ({}));
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		delegates: { known: true, total: 3, active: 3, completed: 0, failed: 0 },
		jobs: { known: true, total: 0, active: 0, completed: 0, failed: 0 },
		watches: { known: true, total: 0, active: 0, completed: 0, failed: 0 },
	}));
	client.on("evener/thread/delegates/list", ({ scope, cursor }) => ({
		context,
		scope: scope ?? "session",
		delegates: cursor ? [delegate("grandchild", "remote:child")] : [delegate("child")],
		page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "next" } : {}) },
	}));
	client.on("evener/thread/jobs/list", ({ scope }) => ({
		context,
		scope: scope ?? "session",
		jobs: [],
		page: { complete: true, issues: [] },
	}));
	return client;
}
test("subtree binding leaves membership to visible page demand and uses authoritative counts", async () => {
	const client = hub();
	const tree = new SubagentTree(context.ref, "root");
	tree.observeActivity();
	await tree.setClient(client);
	expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list").map((c) => c.params)).toEqual([
		{ ref: context.ref, scope: "subtree" },
	]);
	expect(tree.getSnapshot()).toMatchObject({ summary: { delegates: { total: 3 } }, partial: true, hasMore: true });
	await tree.loadMore();
	expect(tree.getSnapshot().tree?.root.entries[0]).toMatchObject({
		kind: "delegate",
		delegate: {
			child: { ref: "remote:child", entries: [{ kind: "delegate", delegate: { delegateId: "grandchild" } }] },
		},
	});
	expect(tree.getSnapshot().partial).toBe(false);
	await tree.setClient(null);
	expect(tree.getSnapshot().tree?.root.entries).toHaveLength(1);
});
test("late old client results cannot replace presentation after reconnect", async () => {
	const old = hub();
	let answer: (value: unknown) => void = () => {};
	let admit: () => void = () => {};
	const entered = new Promise<void>((resolve) => {
		admit = resolve;
	});
	old.on("evener/thread/delegates/list", () => {
		admit();
		return new Promise((resolve) => {
			answer = resolve;
		}) as never;
	});
	const tree = new SubagentTree(context.ref, "root");
	tree.observeActivity();
	const pending = tree.setClient(old);
	await entered;
	const next = hub();
	await tree.setClient(next);
	answer({ context, scope: "subtree", delegates: [delegate("obsolete")], page: { complete: true, issues: [] } });
	await pending;
	expect(tree.getSnapshot().tree?.root.entries[0]).toMatchObject({
		kind: "delegate",
		delegate: { delegateId: "child" },
	});
	await tree.setClient(null);
});

test("summary-only holder never scans, and detail release leaves the badge subscribed", async () => {
	const { subagentTree, holdSubagentTree, forgetSubagentTrees } = await import("./subagentTree");
	const client = hub();
	const tree = subagentTree("summary-hub", context.ref, "root");
	const badge = holdSubagentTree(tree, { collections: false });
	await tree.setClient(client);
	expect(
		client.calls.some(
			(call) => call.method === "evener/thread/delegates/list" || call.method === "evener/thread/jobs/list",
		),
	).toBe(false);
	const detail = holdSubagentTree(tree);
	await tree.reload();
	expect(client.calls.some((call) => call.method === "evener/thread/delegates/list")).toBe(true);
	detail();
	detail();
	await tree.reload();
	expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
	badge();
	await new Promise((resolve) => setTimeout(resolve, 0));
	expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
	forgetSubagentTrees("summary-hub");
});

test("retained rows retire when the routing alias proves a replacement session", async () => {
	const client = hub();
	const tree = new SubagentTree(context.ref, "root");
	tree.observeActivity();
	await tree.setClient(client);
	let entered: () => void = () => {};
	const waiting = new Promise<void>((resolve) => {
		entered = resolve;
	});
	client.on("evener/thread/delegates/list", () => {
		entered();
		return new Promise(() => {});
	});
	client.on("evener/thread/jobs/list", () => new Promise(() => {}));
	client.on("evener/thread/activity/read", ({ scope }) => ({
		context: {
			...context,
			sessionId: "replacement",
			ref: "remote:replacement",
			rootRef: "remote:replacement",
			epoch: "two",
		},
		scope: scope ?? "session",
		delegates: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
		jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
		watches: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
	}));
	const changed = new Promise<void>((resolve) => {
		const stop = tree.subscribe(() => {
			if (tree.getSnapshot().summary?.context.sessionId === "replacement") {
				stop();
				resolve();
			}
		});
	});
	client.emitNotification({ method: "evener/thread/resync", params: { ref: context.ref, threadId: "replacement" } });
	await waiting;
	await changed;
	expect(tree.getSnapshot().tree).toBeNull();
	await tree.setClient(null);
});
