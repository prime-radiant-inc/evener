// Restart/reconnect recovery for in-flight mutations, end to end on the real
// landed runtime + the real conversation store, under the operator's
// client-owned ruling.
//
// The store-side projection is slice 7a (conversation.test.ts). This suite
// pins the PRODUCTION composition 7b wires: the durable pending-row seam
// (createConversationMutationPendingPort) over the real NativeMutationRuntime,
// bound to the real store, so the four recovery semantics hold with the real
// storage, dispatcher and dispatch gate in the loop:
//   1. a mutation the server has acknowledged settles exactly once across
//      restart/reconnect replays - no double settlement, no re-dispatch;
//   2. an unknown outcome stays blocked and never re-dispatches blind;
//   3. a never-attempted record dispatches only when readiness holds, never
//      eagerly on boot;
//   4. the durable rows' storage identity stays isolated from hub/ref
//      identities across restarts.

import { afterEach, expect, test, vi } from "vitest";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { hydrateThread } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import type { SqliteSync } from "./sqliteSync";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";
import { NativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { createConversationMutationPendingPort } from "../../mobile/src/state/conversationMutation";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { projectConversation, type MobileConversation } from "./projectedRows";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
// A deterministic, unique id per call: IDs must be unique per test without
// depending on Math.random (which could collide and flake).
const uuidCounter = vi.hoisted(() => ({ next: 0 }));
vi.mock("expo-crypto", () => ({
	randomUUID: () => `test-uuid-${++uuidCounter.next}`,
	getRandomValues: (array: Uint8Array) => array,
}));

let database: SqliteDoubleDatabase | undefined;
// Every runtime a test creates, stopped before its database closes so no
// timer or dispatch chain outlives the test.
const runtimes: NativeMutationRuntime[] = [];
afterEach(async () => {
	for (const runtime of runtimes.splice(0)) {
		await runtime.stop().catch(() => undefined);
	}
	database?.close();
	database = undefined;
});

function openDatabase(): SqliteSync {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	return opened.port;
}

function thread(): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "hello",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 0,
		updatedAt: 0,
		status: { type: "idle" },
		cwd: "",
		cliVersion: "",
		source: "",
		turns: [],
		evener: {
			ref: "ref-1",
			capabilities: {},
			queue: { revision: 0, depth: 0, preview: [] },
		},
	} as unknown as Thread;
}

function conversation(): MobileConversation {
	return projectConversation(hydrateThread({ thread: thread() }, "ref-1", 0));
}

// The minimal ConversationService the store's open() reads: open(ref) plus a
// notification subscription. Everything else is unused by this path.
function fakeService(model: MobileConversation) {
	return {
		open: async (ref: string) => ({ ...model, ref }),
		subscribeNotifications: () => () => {},
	} as never;
}

function appliedReceipt(params: unknown): never {
	const { clientMutationId } = params as { clientMutationId: string };
	return {
		receipt: {
			clientMutationId,
			disposition: "applied",
			threadId: "thread-1",
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	} as never;
}

function readResponse(
	targetRef: string,
	options: { authoritative?: boolean; ids?: string[]; resumeRequired?: boolean } = {},
): ThreadReadResponse {
	return {
		thread: {
			id: "thread-1",
			status: { type: "ready" },
			evener: {
				ref: targetRef,
				capabilities: {},
				queue: { clientMutationIds: options.ids ?? [] },
				mutationStateAuthoritative: options.authoritative ?? true,
				resumeRequired: options.resumeRequired,
			},
		},
	} as ThreadReadResponse;
}

// Poll until an asynchronous condition holds instead of assuming a fixed number
// of ticks: a storage subscription and the dispatcher's fire-and-forget chains
// settle across an unbounded number of microtasks/macrotasks under load.
async function waitFor(condition: () => boolean, what: string): Promise<void> {
	const deadline = Date.now() + 2000;
	for (;;) {
		if (condition()) return;
		if (Date.now() > deadline) throw new Error(`waitFor timed out: ${what}`);
		await new Promise((resolve) => setTimeout(resolve, 0));
	}
}

async function seed(runtime: NativeMutationRuntime, targetRef: string) {
	const record = await runtime.storage.enqueueIntent({
		targetRef,
		method: "turn/start",
		payload: { ref: "ref-1", input: [{ type: "text", text: "hello" }] },
		attachments: [],
		optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "hello" }] },
	});
	// A direct storage write publishes no change; the runtime's own zero-row
	// discard is the landed way to notify its storage listeners, so a bound
	// seam re-reads the seeded row the way a real enqueue's notify would.
	await runtime.discardRecovery("seed-notify", targetRef);
	return record;
}

function turnStarts(client: FakeClient): number {
	return client.calls.filter((call) => call.method === "turn/start").length;
}

// The production composition: a real runtime bound to one target, a real store
// opened on that conversation, and the store's durable pending seam bound to
// the runtime through the production port factory.
async function composition(hubId = "hub-1", targetRef = "ref-1") {
	const runtime = new NativeMutationRuntime(openDatabase(), {});
	runtimes.push(runtime);
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	runtime.registerTarget(hubId, targetRef, client);
	await runtime.start();
	const store = createConversationStore();
	await store.getState().open(fakeService(conversation()) as never, targetRef);
	const key = nativeMutationTargetKey(hubId, targetRef);
	const port = createConversationMutationPendingPort(runtime, key);
	store.getState().bindPendingMutations(port);
	await waitFor(() => Array.isArray(store.getState().pendingMutations), "the initial read publishes");
	return { runtime, client, store, key };
}

test("(3) a never-attempted record dispatches only when readiness holds, never eagerly on boot", async () => {
	const { runtime, client, store, key } = await composition();
	const record = await seed(runtime, key);
	await waitFor(
		() => store.getState().pendingMutations?.some((row) => row.id === record.clientMutationId) === true,
		"the durable row is visible",
	);

	// Boot: the durable row is visible, but nothing has left the client. The
	// target gate is closed until an authoritative read opens it.
	expect(store.getState().pendingMutations?.map((row) => row.id)).toEqual([
		record.clientMutationId,
	]);
	expect(turnStarts(client)).toBe(0);

	// Readiness: the matching authoritative read opens the gate and the record
	// dispatches.
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	expect(lease).toBeDefined();
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1", { ids: [] }));
	await waitFor(() => turnStarts(client) === 1, "the record dispatches once ready");
	expect(turnStarts(client)).toBe(1);
});

test("(1) a server-acknowledged mutation settles exactly once across restart/reconnect replays", async () => {
	const { runtime, client, store, key } = await composition();
	const record = await seed(runtime, key);
	await waitFor(() => store.getState().pendingMutations?.length === 1, "the durable row is visible");
	expect(store.getState().pendingMutations).toHaveLength(1);

	// The server acknowledges it: the authoritative read names the id, which
	// settles the durable record out of storage exactly once.
	const first = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(first!, readResponse("ref-1", { ids: [record.clientMutationId] }));
	await waitFor(() => store.getState().pendingMutations?.length === 0, "the settled row drops");
	expect(store.getState().pendingMutations).toEqual([]);
	await expect(runtime.storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();

	// A reconnect replay: the same authoritative read re-runs. The settled
	// record neither resurrects nor re-dispatches.
	const replay = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(replay!, readResponse("ref-1", { ids: [record.clientMutationId] }));
	await waitFor(() => store.getState().pendingMutations?.length === 0, "the replay does not resurrect");
	expect(store.getState().pendingMutations).toEqual([]);
	expect(turnStarts(client)).toBe(0);
	await expect(runtime.storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
});

test("(2) an unknown outcome stays blocked, never auto-resumes, and surfaces through the stack", async () => {
	const { runtime, client, store, key } = await composition();
	const record = await seed(runtime, key);
	// The unknown outcome: the dispatcher's own attempted-then-blocked path.
	await runtime.storage.markAttempted(record.clientMutationId);
	const blockedRead = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(blockedRead!, readResponse("ref-1", { authoritative: false }));
	await waitFor(
		() => store.getState().pendingMutations?.[0]?.state === "blockedUnknown",
		"the blocked row surfaces",
	);

	// Surfaces through the landed stack: the store's pending row carries the
	// blockedUnknown state.
	expect(store.getState().pendingMutations?.[0]).toMatchObject({
		id: record.clientMutationId,
		state: "blockedUnknown",
	});
	expect(turnStarts(client)).toBe(0);

	// A later never-attempted record behind the blocked head parks: the FIFO
	// never re-dispatches blind past an unknown outcome. The reconnect read here
	// is deliberately NON-authoritative (the server has not vouched for the
	// mutation state), so the blocked record is not provably absent and stays
	// blocked.
	const later = await seed(runtime, key);
	const open = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(open!, readResponse("ref-1", { authoritative: false }));
	await waitFor(
		() => store.getState().pendingMutations?.some((row) => row.id === later.clientMutationId) === true,
		"the later row is visible",
	);
	expect(turnStarts(client)).toBe(0);
	expect((await runtime.storage.getOutbox(record.clientMutationId))?.state).toBe("blockedUnknown");
	expect((await runtime.storage.getOutbox(later.clientMutationId))?.state).toBe("submitting");
});

test("(4) storage/hub-ref identities stay isolated across a restart", async () => {
	const db = openDatabase();
	const runtime = new NativeMutationRuntime(db, {});
	runtimes.push(runtime);
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	runtime.registerTarget("hub-a", "ref-1", client);
	await runtime.start();

	const keyA = nativeMutationTargetKey("hub-a", "ref-1");
	const keyB = nativeMutationTargetKey("hub-b", "ref-1");
	// Same wire ref, different hubs: the storage keys are distinct and belong to
	// no hub/ref namespace.
	expect(keyA).not.toBe(keyB);
	expect([keyA, keyB]).not.toContain("ref-1");
	expect([keyA, keyB]).not.toContain("hub-a");
	expect([keyA, keyB]).not.toContain("hub-b");

	await seed(runtime, keyA);
	const foreign = await seed(runtime, keyB);

	// First app run: the store for hub-a only ever shows hub-a's row.
	const store = createConversationStore();
	await store.getState().open(fakeService(conversation()) as never, "ref-1");
	store.getState().bindPendingMutations(createConversationMutationPendingPort(runtime, keyA));
	await waitFor(() => store.getState().pendingMutations?.length === 1, "hub-a's own row shows");
	expect(store.getState().pendingMutations).toHaveLength(1);

	// Restart: a fresh store over the same durable storage, rebound for hub-a.
	const restarted = createConversationStore();
	await restarted.getState().open(fakeService(conversation()) as never, "ref-1");
	restarted.getState().bindPendingMutations(createConversationMutationPendingPort(runtime, keyA));
	await waitFor(() => restarted.getState().pendingMutations?.length === 1, "the restarted store restores hub-a's row");
	expect(restarted.getState().pendingMutations).toHaveLength(1);
	expect(restarted.getState().pendingMutations?.map((row) => row.id)).not.toContain(
		foreign.clientMutationId,
	);
});

test("(1/restart) a dispatched-but-unsettled mutation survives a restart and settles without re-dispatch", async () => {
	const dir = mkdtempSync(join(tmpdir(), "evener-inflight-"));
	const file = join(dir, "evener-mutations.db");
	const key = nativeMutationTargetKey("hub-1", "ref-1");
	try {
		// ---- first process: dispatch, but never settle (the wire outcome
		// hangs, so the record stays attempted and in-flight).
		const opened = openSqliteSyncDouble(file);
		const first = new NativeMutationRuntime(opened.port, {});
		runtimes.push(first);
		const firstClient = new FakeClient("ready");
		firstClient.on("turn/start", () => new Promise<never>(() => {}));
		first.registerTarget("hub-1", "ref-1", firstClient);
		await first.start();
		const record = await seed(first, key);
		const lease = first.beginAuthoritativeRead("hub-1", "ref-1", firstClient);
		await first.reconcileAuthoritativeRead(lease!, readResponse("ref-1", { ids: [] }));
		await waitFor(() => turnStarts(firstClient) === 1, "the mutation reaches the client");
		const inFlight = await first.storage.getOutbox(record.clientMutationId);
		expect(inFlight?.state).toBe("submitting");
		expect(inFlight?.attempted).toBe(true);
		await first.stop();
		opened.database.close();

		// ---- restart: reopen the same file, fresh runtime + client.
		const reopened = openSqliteSyncDouble(file);
		const second = new NativeMutationRuntime(reopened.port, {});
		runtimes.push(second);
		const secondClient = new FakeClient("ready");
		secondClient.on("turn/start", appliedReceipt);
		second.registerTarget("hub-1", "ref-1", secondClient);
		await second.start();
		const store = createConversationStore();
		await store.getState().open(fakeService(conversation()) as never, "ref-1");
		store.getState().bindPendingMutations(createConversationMutationPendingPort(second, key));
		await waitFor(
			() => store.getState().pendingMutations?.length === 1,
			"the in-flight row survives the restart",
		);

		// Boot does not re-dispatch it: the gate stays closed until an
		// authoritative read opens it.
		expect(turnStarts(secondClient)).toBe(0);

		// The server acknowledges it: the authoritative read naming it settles
		// the durable row exactly once, with no second client call.
		const settle = second.beginAuthoritativeRead("hub-1", "ref-1", secondClient);
		await second.reconcileAuthoritativeRead(
			settle!,
			readResponse("ref-1", { ids: [record.clientMutationId] }),
		);
		await waitFor(() => store.getState().pendingMutations?.length === 0, "the acknowledged row removes");
		expect(store.getState().pendingMutations).toEqual([]);
		expect(turnStarts(secondClient)).toBe(0);
		await expect(second.storage.getOutbox(record.clientMutationId)).resolves.toBeUndefined();
		await second.stop();
		reopened.database.close();
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});

test("(restart) a fresh runtime over a reopened database restores the durable rows and reconciles once", async () => {
	const dir = mkdtempSync(join(tmpdir(), "evener-restart-"));
	const file = join(dir, "evener-mutations.db");
	const key = nativeMutationTargetKey("hub-1", "ref-1");
	try {
		// ---- first process -------------------------------------------------
		const opened = openSqliteSyncDouble(file);
		const first = new NativeMutationRuntime(opened.port, {});
		runtimes.push(first);
		const firstClient = new FakeClient("ready");
		firstClient.on("turn/start", appliedReceipt);
		first.registerTarget("hub-1", "ref-1", firstClient);
		await first.start();

		// The unknown-outcome mutation is seeded FIRST so it is the FIFO head; it
		// is blocked through the runtime's own attempted-then-blocked path (a
		// non-authoritative read: the server has not vouched for the mutation
		// state), and the never-attempted mutation parks behind it. The process
		// exits with both rows durable.
		const unknown = await seed(first, key);
		const acked = await seed(first, key);
		await first.storage.markAttempted(unknown.clientMutationId);
		const blockedRead = first.beginAuthoritativeRead("hub-1", "ref-1", firstClient);
		await first.reconcileAuthoritativeRead(blockedRead!, readResponse("ref-1", { authoritative: false }));
		// The blocked head parks the never-attempted record behind it: nothing
		// left the first process either.
		expect(turnStarts(firstClient)).toBe(0);
		await first.stop();
		opened.database.close();

		// ---- restart: reopen the SAME database file, fresh runtime + client --
		const reopened = openSqliteSyncDouble(file);
		const second = new NativeMutationRuntime(reopened.port, {});
		runtimes.push(second);
		const secondClient = new FakeClient("ready");
		secondClient.on("turn/start", appliedReceipt);
		second.registerTarget("hub-1", "ref-1", secondClient);
		await second.start();

		const store = createConversationStore();
		await store.getState().open(fakeService(conversation()) as never, "ref-1");
		store.getState().bindPendingMutations(createConversationMutationPendingPort(second, key));
		await waitFor(
			() => store.getState().pendingMutations?.length === 2,
			"both durable rows are restored",
		);

		// Dispatch behavior on boot: nothing leaves the client.
		expect(turnStarts(secondClient)).toBe(0);

		// Pending-row restoration across the reopen: both durable rows come back,
		// the unknown one still blocked.
		const rows = store.getState().pendingMutations ?? [];
		expect(rows.map((row) => row.id)).toContain(unknown.clientMutationId);
		expect(rows.find((row) => row.id === unknown.clientMutationId)).toMatchObject({
			state: "blockedUnknown",
		});
		expect(rows.find((row) => row.id === acked.clientMutationId)).toMatchObject({
			state: "submitting",
		});

		// Settlement across the restart: the authoritative read that names both
		// ids settles them exactly once, with no re-dispatch, and a replay does
		// not resurrect them.
		const settle = second.beginAuthoritativeRead("hub-1", "ref-1", secondClient);
		await second.reconcileAuthoritativeRead(
			settle!,
			readResponse("ref-1", { ids: [acked.clientMutationId, unknown.clientMutationId] }),
		);
		await waitFor(() => store.getState().pendingMutations?.length === 0, "both settle out");
		expect(store.getState().pendingMutations).toEqual([]);
		expect(turnStarts(secondClient)).toBe(0);
		await expect(second.storage.getOutbox(acked.clientMutationId)).resolves.toBeUndefined();
		await expect(second.storage.getOutbox(unknown.clientMutationId)).resolves.toBeUndefined();
		const replay = second.beginAuthoritativeRead("hub-1", "ref-1", secondClient);
		await second.reconcileAuthoritativeRead(
			replay!,
			readResponse("ref-1", { ids: [acked.clientMutationId, unknown.clientMutationId] }),
		);
		await waitFor(() => store.getState().pendingMutations?.length === 0, "the replay does not resurrect");
		expect(store.getState().pendingMutations).toEqual([]);
		await second.stop();
		reopened.database.close();
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
});
