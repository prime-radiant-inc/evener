import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { SubagentTree, subagentTree, holdSubagentTree, forgetSubagentTrees } from "./subagentTree";
import { installActivityFixture } from "./sessionActivityTestUtils";
const session = (entries: unknown[], branch: Record<string, unknown> = {}) => ({
	kind: "session",
	sessionId: "coord",
	ref: "local:coord",
	label: "Coordinator",
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	entries,
	branch,
});
const delegate = (id: string) => ({
	kind: "delegate",
	delegate: {
		delegateId: id,
		childSessionId: id,
		childRef: `local:${id}`,
		type: "delegate",
		description: id,
		branch: {},
	},
});
const whole = { revision: 1, root: session([delegate("a")]) };
const first = { revision: 1, root: session([delegate("a"), delegate("b")], { truncated: true, continuation: "next" }) };
const second = { revision: 1, root: session([delegate("c")]) };
function hub(read: (cursor?: string) => unknown | Promise<unknown> = () => whole) {
	const client = new FakeClient("ready");
	installActivityFixture(client, read);
	client.on("thread/read", () => ({ thread: { id: "coord", modelProvider: "scripted/model" } }) as never);
	return client;
}
function detail() {
	const tree = new SubagentTree("local:coord", "coord");
	tree.observeActivity();
	return tree;
}
const ids = (tree: SubagentTree) =>
	tree
		.getSnapshot()
		.tree?.root.entries.map((entry) => (entry.kind === "delegate" ? entry.delegate.delegateId : entry.job.jobId));
const reads = (client: FakeClient) => client.calls.filter((call) => call.method === "evener/thread/delegates/list");
afterEach(() => {
	forgetSubagentTrees("one");
	forgetSubagentTrees("two");
	vi.useRealTimers();
});
test("retains the last accepted membership across disconnect", async () => {
	const tree = detail();
	expect(tree.getSnapshot().tree).toBeNull();
	await tree.setClient(hub());
	expect(ids(tree)).toEqual(["a"]);
	await tree.setClient(null);
	expect(ids(tree)).toEqual(["a"]);
	expect(tree.getSnapshot().loading).toBe(false);
});
test("only explicit visible demand reads continuation and merges membership", async () => {
	const client = hub((cursor) => (cursor ? second : first));
	const tree = detail();
	await tree.setClient(client);
	expect(reads(client).map((call) => call.params)).toEqual([{ ref: "local:coord", scope: "subtree" }]);
	expect(ids(tree)).toEqual(["a", "b"]);
	expect(tree.getSnapshot().partial).toBe(true);
	await tree.loadMore();
	expect(ids(tree)).toEqual(["a", "b", "c"]);
	expect(tree.getSnapshot().partial).toBe(false);
	await tree.setClient(null);
});
test("an empty incomplete page stays unknown and advances to admitted rows", async () => {
	const tree = detail();
	await tree.setClient(
		hub((cursor) => (cursor ? whole : { revision: 1, root: session([], { truncated: true, continuation: "next" }) })),
	);
	expect(ids(tree)).toEqual(["a"]);
	expect(tree.getSnapshot().partial).toBe(false);
	await tree.setClient(null);
});
test("a failed continuation keeps accepted rows across client failure and disconnect", async () => {
	const tree = detail();
	await tree.setClient(
		hub((cursor) => {
			if (cursor) throw new Error("source offline");
			return first;
		}),
	);
	await tree.loadMore();
	expect(ids(tree)).toEqual(["a", "b"]);
	expect(tree.getSnapshot().partial).toBe(true);
	await tree.setClient(
		hub(() => {
			throw new Error("source offline");
		}),
	);
	expect(ids(tree)).toEqual(["a", "b"]);
	await tree.setClient(null);
	expect(tree.getSnapshot().partial).toBe(true);
});
test("source issues stay visible in retained presentation while disconnected", async () => {
	const tree = detail();
	await tree.setClient(hub(() => ({ revision: 1, root: session([delegate("a")], { error: "branch unavailable" }) })));
	expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["local:coord"] });
	await tree.setClient(null);
	expect(tree.getSnapshot().missing).toEqual(["local:coord"]);
});
test("a replacement client can read while the old external transport is stuck", async () => {
	let admit = () => {};
	const admitted = new Promise<void>((resolve) => {
		admit = resolve;
	});
	let answer: (value: unknown) => void = () => {};
	const old = hub(() => {
		admit();
		return new Promise((resolve) => {
			answer = resolve;
		});
	});
	const tree = detail();
	const pending = tree.setClient(old);
	await admitted;
	await tree.setClient(hub(() => second));
	expect(ids(tree)).toEqual(["c"]);
	answer(whole);
	await pending;
	expect(ids(tree)).toEqual(["c"]);
	await tree.setClient(null);
});
test("retains the last tree until a same-session reconnect supplies membership", async () => {
	const tree = detail();
	await tree.setClient(hub());
	let admit = () => {};
	const admitted = new Promise<void>((resolve) => {
		admit = resolve;
	});
	let answer: (value: unknown) => void = () => {};
	const pending = tree.setClient(
		hub(() => {
			admit();
			return new Promise((resolve) => {
				answer = resolve;
			});
		}),
	);
	await admitted;
	expect(ids(tree)).toEqual(["a"]);
	answer(second);
	await pending;
	expect(ids(tree)).toEqual(["c"]);
	await tree.setClient(null);
});
test("follow uses additive membership and keeps a model through same-session disconnect", async () => {
	const tree = detail();
	const client = hub();
	await tree.setClient(client);
	await tree.follow();
	expect(client.calls.find((call) => call.method === "thread/read")?.params).toMatchObject({
		ref: "local:coord",
		subscribe: true,
		replaceSubscription: false,
	});
	expect(tree.getSnapshot().coordinatorModel).toBe("scripted/model");
	await tree.setClient(null);
	expect(tree.getSnapshot().coordinatorModel).toBe("scripted/model");
});
test("shared holders are ref scoped and the final holder alone disposes reads", async () => {
	const tree = subagentTree("one", "local:coord", "coord");
	expect(subagentTree("one", "local:coord", "new-route-hint")).toBe(tree);
	expect(subagentTree("two", "local:coord", "coord")).not.toBe(tree);
	const release = holdSubagentTree(tree);
	const other = holdSubagentTree(tree);
	const client = hub();
	await tree.setClient(client);
	other();
	other();
	await tree.reload();
	expect(ids(tree)).toEqual(["a"]);
	const unsubscribed = new Promise<void>((resolve) =>
		client.on("thread/unsubscribe", () => {
			resolve();
			return {};
		}),
	);
	release();
	await unsubscribed;
	expect(ids(tree)).toEqual(["a"]);
	expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
});
