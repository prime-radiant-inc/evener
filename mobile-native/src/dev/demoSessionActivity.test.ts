import { describe, expect, it, vi } from "vitest";
import { parseActivityTree, SessionActivityStore } from "@evener/appwire-client";
import { FakeClient, callsTo } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { createDemoFleet, demoSessionId } from "./demoFleet";

import { createDemoSessionActivity } from "./demoSessionActivity";
import { demoActivityTree } from "./demoSubagents";

const ref = `local:${demoSessionId("s-pr2138")}`;
const params = { ref, scope: "subtree" as const, limit: 1 };
const invalid = { code: -32602, data: { evenerErrorInfo: "invalidParams" } };
const fleet = () => createDemoFleet({ now: Date.parse("2026-09-26T18:00:00.000Z") });

function continuation(source: ReturnType<typeof fleet>) {
	const first = source.answerDelegatesList(params);
	if (!first.page.nextCursor) throw new Error("missing demo continuation");
	return { first, cursor: first.page.nextCursor };
}

describe("bounded demo activity continuations", () => {
	it("retains 128 snapshots and classifies an evicted issued continuation as stale", () => {
		const source = fleet();
		const { cursor } = continuation(source);
		for (let i = 0; i < 127; i++) continuation(source);
		expect(source.answerDelegatesList({ ...params, cursor, limit: 200 }).delegates).toHaveLength(54);
		continuation(source);
		expect(() => source.answerDelegatesList({ ...params, cursor })).toThrowError(
			expect.objectContaining({ code: -32602, data: { evenerErrorInfo: "sessionActivityCursorStale" } }),
		);
	});

	it("keeps snapshot membership independent of the continuation row limit", () => {
		const source = fleet();
		const { first, cursor } = continuation(source);
		const rest = source.answerDelegatesList({ ...params, cursor, limit: 200 });
		expect(rest.page.complete).toBe(true);
		expect(new Set([...first.delegates, ...rest.delegates].map((row) => row.delegateId)).size).toBe(55);
	});

	it.each([false, true])("rejects cross-binding tokens even when evicted=%s", (evicted) => {
		const source = fleet();
		const { first, cursor } = continuation(source);
		const childRef = first.delegates[0]?.childRef;
		if (!childRef) throw new Error("missing genuine child ref");
		if (evicted) for (let i = 0; i < 129; i++) continuation(source);
		for (const request of [
			() => source.answerSessionJobsList({ ...params, cursor }),
			() => source.answerWatchesList({ ...params, cursor }),
			() => source.answerDelegatesList({ ...params, ref: childRef, cursor }),
			() => source.answerDelegatesList({ ref, cursor }),
		])
			expect(request).toThrowError(expect.objectContaining(invalid));
	});

	it("rejects malformed, unsupported and never-issued tokens", () => {
		const source = fleet();
		continuation(source);
		for (const cursor of [
			"malformed",
			"demo-activity-1",
			"null",
			"[]",
			"{}",
			JSON.stringify({ version: 2, sequence: 1, ref, scope: "subtree", resource: "delegates" }),
			JSON.stringify({ version: 1, sequence: 1, ref, scope: "subtree" }),
			JSON.stringify({ version: 1, sequence: 1, ref, scope: "subtree", resource: "delegates", extra: true }),
			JSON.stringify({ version: 1, sequence: 1, ref: null, scope: "subtree", resource: "delegates" }),
			...[0, -1, 1.5, 2, Number.MAX_SAFE_INTEGER + 1].map((sequence) =>
				JSON.stringify({ version: 1, sequence, ref, scope: "subtree", resource: "delegates" }),
			),
		])
			expect(() => source.answerDelegatesList({ ...params, cursor })).toThrowError(expect.objectContaining(invalid));
	});

	it.each(["malformed", "cross-binding"])("permanently parks %s continuation errors without spinning", async (kind) => {
		vi.useFakeTimers();
		const source = fleet();
		const client = new FakeClient("ready");
		client.on("thread/read", () => ({ thread: wireThread(ref, { id: demoSessionId("s-pr2138") }) }));
		client.on("thread/unsubscribe", () => ({}));
		client.on("evener/thread/activity/read", source.answerActivityRead);
		client.on("evener/thread/delegates/list", (request) => {
			if (!request.cursor) return source.answerDelegatesList(request);
			return source.answerDelegatesList(
				kind === "malformed" ? { ...request, cursor: "malformed" } : { ...request, scope: "session" },
			);
		});
		const store = new SessionActivityStore(client, ref, {
			scope: "subtree",
			clock: {
				setTimeout: (callback, ms) => setTimeout(callback, ms),
				clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
			},
		});
		store.start();
		const release = store.observe("delegates");
		try {
			await Promise.all([store.refresh("summary"), store.load("delegates")]);
			await store.loadMore("delegates");
			expect(store.getSnapshot().delegates).toMatchObject({ permanent: true, error: invalid });
			expect(store.getSnapshot().delegates.rows).toHaveLength(50);
			const admitted = callsTo(client, "evener/thread/delegates/list");
			await vi.advanceTimersByTimeAsync(120000);
			client.emitStateChange("reconnecting");
			client.emitReady();
			await store.refresh("summary");
			expect(callsTo(client, "evener/thread/delegates/list")).toBe(admitted);
		} finally {
			release();
			store.dispose();
			vi.useRealTimers();
		}
	});
});

// The hub closes a finished delegate that can't be resumed: its phase is
// "closed", not "idle".
it("reports a finished delegate that can't be resumed as closed", () => {
	const rows = fleet().answerDelegatesList({ ...params, limit: 200 }).delegates;
	const finished = rows.filter((row) => row.terminal);
	expect(finished.length).toBeGreaterThan(0);
	for (const row of finished) expect(row).toMatchObject({ resumable: false, phase: "closed" });
	for (const row of rows.filter((candidate) => !candidate.terminal)) expect(row.phase).toBe("running");
});

it("preserves the producer's finished report and truncation through typed demo reads", () => {
	const tree = parseActivityTree(
		demoActivityTree(
			{
				ref: "local:demo",
				title: "Demo",
				model: "demo-model",
				subagentRef: (id) => `local:${id}`,
				subagents: [{ id: "done", title: "Check", state: "done", ago: 1, line: "Tests pass" }],
			},
			Date.parse("2026-09-26T18:00:00Z"),
		).data,
	);
	if (!tree) throw new Error("missing producer tree");
	const entry = tree.root.entries.find((row) => row.kind === "delegate");
	if (!entry || entry.kind !== "delegate") throw new Error("missing producer delegate");
	entry.delegate.reportPreviewTruncated = true;
	const source = createDemoSessionActivity(() => ({ tree, availability: "live" }), "epoch");
	const row = source.delegates({ ref: tree.root.ref }).delegates[0];
	expect(entry.delegate.reportPreview).toBe("Tests pass.");
	expect(row?.reportPreview).toBe(entry.delegate.reportPreview);
	expect(row?.reportPreviewTruncated).toBe(true);
	expect(row?.runGeneration).toBe(entry.delegate.runGeneration);
});
