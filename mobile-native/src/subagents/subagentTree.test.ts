import { afterEach, describe, expect, it, vi } from "vitest";
import { ACTIVITY_REFRESH_MIN_INTERVAL_MS } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { forgetSubagentTrees, holdSubagentTree, SubagentTree, subagentTree } from "./subagentTree";

const session = (entries: unknown[], branch: Record<string, unknown> = {}) => ({
	kind: "session",
	sessionId: "coord",
	ref: "local:coord",
	label: "Get PR 2138 Test Clean",
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	entries,
	branch,
});
const subagent = (id: string) => ({
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
const firstPage = {
	revision: 1,
	root: session([subagent("a"), subagent("b")], { truncated: true, continuation: "page-2" }),
};
const secondPage = { revision: 1, root: session([subagent("c")]) };
const whole = { revision: 1, root: session([subagent("a")]) };

function hub(pages: (continuation: string | undefined) => unknown) {
	const client = new FakeClient("ready");
	client.on("evener/jobs/list", async (params) => ({ data: await pages(params.continuation) }));
	return client;
}
const listed = (tree: SubagentTree) =>
	tree
		.getSnapshot()
		.tree?.root.entries.map((entry) => (entry.kind === "delegate" ? entry.delegate.delegateId : entry.job.jobId));
const reads = (client: FakeClient) =>
	client.calls
		.filter((call) => call.method === "evener/jobs/list")
		.map((call) => (call.params as { continuation?: string }).continuation ?? "root");
const treeUpdated = (client: FakeClient, ref = "local:coord", threadId = "coord") =>
	client.emitNotification({ method: "evener/jobs/treeUpdated", params: { threadId, ref, revision: 2 } });

const delegateUpdated = (client: FakeClient, delegateId: string, projectionRevision: number, phase = "running") =>
	client.emitNotification({
		method: "evener/delegate/updated",
		params: {
			ref: "local:coord",
			threadId: "coord",
			delegate: {
				delegateId,
				ownerSessionId: "coord",
				rootSessionId: "coord",
				childSessionId: delegateId,
				transcriptRef: `local:${delegateId}`,
				type: "delegate",
				lifecycle: "running",
				phase,
				status: "running",
				terminal: false,
				resumable: false,
				needsAttention: false,
				projectionRevision,
			},
		},
	} as never);
const heldPhase = (tree: SubagentTree) => {
	const entry = tree.getSnapshot().tree?.root.entries[0];
	return entry?.kind === "delegate" ? entry.delegate.phase : undefined;
};

afterEach(() => {
	vi.useRealTimers();
});

describe("one coordinator's subagent tree", () => {
	it("reads the tree when it gets a client, and keeps it when the client goes", async () => {
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		expect(tree.getSnapshot()).toMatchObject({ tree: null, loading: false });
		const read = tree.setClient(client);
		expect(tree.getSnapshot().loading).toBe(true);
		await read;
		expect(listed(tree)).toEqual(["a"]);
		await tree.setClient(null);
		expect(listed(tree)).toEqual(["a"]);
		expect(tree.getSnapshot().loading).toBe(false);
	});

	it("follows every page, so the list and its counts are the whole tree's (Review Focus 1)", async () => {
		const client = hub((continuation) => (continuation === "page-2" ? secondPage : firstPage));
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		expect(reads(client)).toEqual(["root", "page-2"]);
		expect(listed(tree)).toEqual(["a", "b", "c"]);
		expect(tree.getSnapshot()).toMatchObject({ partial: false, missing: [] });
	});

	it("tries a failing page once per reload and says what it couldn't list (Review Focus 1)", async () => {
		const client = hub((continuation) => {
			if (continuation === "page-2") throw new Error("offline");
			return firstPage;
		});
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		expect(reads(client)).toEqual(["root", "page-2"]);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
		await tree.reload();
		expect(reads(client)).toEqual(["root", "page-2", "root", "page-2"]);
	});

	it("reads through a new client even while the old client's read never answers", async () => {
		const stuck = new FakeClient("ready");
		stuck.on("evener/jobs/list", () => new Promise(() => {}));
		const tree = new SubagentTree("local:coord", "coord");
		void tree.setClient(stuck);
		await tree.setClient(hub(() => whole));
		expect(listed(tree)).toEqual(["a"]);
	});

	it("calls its count partial while later pages are still loading", async () => {
		let answer: (page: unknown) => void = () => {};
		const client = hub((continuation) =>
			continuation === "page-2" ? new Promise((resolve) => (answer = resolve)) : firstPage,
		);
		const tree = new SubagentTree("local:coord", "coord");
		const read = tree.setClient(client);
		await new Promise((resolve) => setTimeout(resolve, 0));
		expect(listed(tree)).toEqual(["a", "b"]);
		expect(tree.getSnapshot().partial).toBe(true);
		answer(secondPage);
		await read;
		expect(tree.getSnapshot()).toMatchObject({ partial: false, missing: [] });
	});

	it("stays partial when the connection drops while later pages are loading", async () => {
		const client = hub((continuation) => (continuation === "page-2" ? new Promise(() => {}) : firstPage));
		const tree = new SubagentTree("local:coord", "coord");
		void tree.setClient(client);
		await new Promise((resolve) => setTimeout(resolve, 0));
		expect(listed(tree)).toEqual(["a", "b"]);
		await tree.setClient(null);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
	});

	it("keeps what a settled read couldn't list when a new client drops before its first page", async () => {
		const client = hub((continuation) => {
			if (continuation === "page-2") throw new Error("offline");
			return firstPage;
		});
		const stuck = new FakeClient("ready");
		stuck.on("evener/jobs/list", () => new Promise(() => {}));
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		void tree.setClient(stuck);
		await tree.setClient(null);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
	});

	it("keeps what a settled read couldn't list when a new client's first read fails", async () => {
		const client = hub((continuation) => {
			if (continuation === "page-2") throw new Error("offline");
			return firstPage;
		});
		const failing = new FakeClient("ready");
		failing.on("evener/jobs/list", () => Promise.reject(new Error("offline")));
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		await tree.setClient(failing);
		expect(listed(tree)).toEqual(["a", "b"]);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
	});

	it("keeps saying what it couldn't list while it's disconnected", async () => {
		const client = hub((continuation) => {
			if (continuation === "page-2") throw new Error("offline");
			return firstPage;
		});
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		await tree.setClient(null);
		expect(tree.getSnapshot()).toMatchObject({ partial: true, missing: ["Get PR 2138 Test Clean"] });
	});

	it("reads again on its coordinator's tree notifications, folding a burst into one more read", async () => {
		vi.useFakeTimers();
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		treeUpdated(client);
		treeUpdated(client);
		treeUpdated(client, "local:other", "other");
		await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(reads(client)).toEqual(["root", "root"]);
	});

	it("applies a burst of updates for a subagent it holds in place, without reading again", async () => {
		vi.useFakeTimers();
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		for (let revision = 2; revision <= 51; revision++) delegateUpdated(client, "a", revision, `step-${revision}`);
		await vi.advanceTimersByTimeAsync(10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(reads(client)).toEqual(["root"]);
		expect(heldPhase(tree)).toBe("step-51");
	});

	it("reads once for a burst of updates about a subagent it doesn't hold, after the minimum interval", async () => {
		vi.useFakeTimers();
		const client = hub(() => whole);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		for (let i = 0; i < 5; i++) delegateUpdated(client, "new", 1);
		await vi.advanceTimersByTimeAsync(1000);
		expect(reads(client)).toEqual(["root"]);
		await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(reads(client)).toEqual(["root", "root"]);
	});

	it("doesn't read back to back while updates keep arriving and reads are slow", async () => {
		vi.useFakeTimers();
		const client = new FakeClient("ready");
		const starts: number[] = [];
		client.on("evener/jobs/list", () => {
			starts.push(Date.now());
			return new Promise((resolve) => setTimeout(() => resolve({ data: whole }), 3000));
		});
		const tree = new SubagentTree("local:coord", "coord");
		void tree.setClient(client);
		for (let at = 0; at < 60_000; at += 500) {
			delegateUpdated(client, "new", 1);
			await vi.advanceTimersByTimeAsync(500);
		}
		// Each read starts at least the minimum interval after the last one ended.
		for (let i = 1; i < starts.length; i++)
			expect((starts[i] ?? 0) - (starts[i - 1] ?? 0)).toBeGreaterThanOrEqual(3000 + ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(starts.length).toBeLessThanOrEqual(13);
	});

	it("follows the coordinator when asked, learning its model", async () => {
		const client = hub(() => whole);
		client.on(
			"thread/read",
			() =>
				({
					thread: {
						id: "coord",
						modelProvider: "lunaroute/glm-5.3-vision",
						status: { type: "active" },
						evener: { ref: "local:coord", capabilities: {}, queue: { revision: 0 } },
					},
				}) as never,
		);
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(client);
		await tree.follow();
		expect(client.calls.find((call) => call.method === "thread/read")?.params).toEqual({
			ref: "local:coord",
			includeTurns: false,
			subscribe: true,
			replaceSubscription: true,
		});
		expect(tree.getSnapshot().coordinatorModel).toBe("lunaroute/glm-5.3-vision");
		expect(reads(client)).toEqual(["root", "root"]);
	});

	it("keeps the last tree on screen through a reconnect until the new read lands", async () => {
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(hub(() => whole));
		const second = new FakeClient("ready");
		second.on("evener/jobs/list", () => new Promise<never>(() => {}));
		void tree.setClient(second);
		expect(listed(tree)).toEqual(["a"]);
		expect(tree.getSnapshot().loading).toBe(false);
	});

	it("takes the new connection's tree even when its revision is lower, as after a daemon restart", async () => {
		const tree = new SubagentTree("local:coord", "coord");
		await tree.setClient(hub(() => ({ revision: 7, root: session([subagent("a")]) })));
		await tree.setClient(hub(() => whole));
		expect(tree.getSnapshot().tree?.revision).toBe(1);
		expect(listed(tree)).toEqual(["a"]);
		await tree.setClient(hub(() => ({ revision: 1, root: session([subagent("a"), subagent("b")]) })));
		expect(listed(tree)).toEqual(["a", "b"]);
	});
});

describe("the shared tree", () => {
	it("is one per hub and coordinator, reads while any screen holds it, and keeps its tree after", async () => {
		vi.useFakeTimers();
		const tree = subagentTree("hub-1", "local:coord", "coord");
		expect(subagentTree("hub-1", "local:coord", "coord")).toBe(tree);
		expect(subagentTree("hub-2", "local:coord", "coord")).not.toBe(tree);
		const client = hub(() => whole);
		const releaseList = holdSubagentTree(tree);
		const releaseSubagent = holdSubagentTree(tree);
		await tree.setClient(client);
		releaseSubagent();
		const before = reads(client).length;
		treeUpdated(client);
		await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(reads(client).length).toBe(before + 1);
		releaseList();
		treeUpdated(client);
		await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
		expect(reads(client).length).toBe(before + 1);
		expect(listed(tree)).toEqual(["a"]);
		forgetSubagentTrees("hub-1");
		expect(subagentTree("hub-1", "local:coord", "coord")).not.toBe(tree);
	});
});
