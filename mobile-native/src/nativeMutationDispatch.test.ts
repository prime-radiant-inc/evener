// The production dispatcher wiring, exercised end to end over the real
// composition the conversation screen mounts: the real ConversationService
// (with its native read fence hooks), the real ConversationStore (with the
// durable mutation submitter), the real NativeMutationRuntime over the real
// SQLite storage adapter, and a FakeClient for the wire. It replaces nothing
// with a double on the native side - only the transport is fake.
//
// What this pins:
// - a rejected production send is admitted durably, dispatched, refused, and
//   transferred to a recovery row the recovery surface reads (the row the
//   landed ConversationScreen.recovery.test.tsx already proves renders an
//   entry for);
// - a durable send clears the composer's durable unconfirmed draft at the
//   enqueue boundary, while the turn is still in flight, so a crash before the
//   daemon answers cannot resurrect the message the outbox already owns.

import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "@evener/appwire-client";
import type {
	AppwireClientLike,
	Thread,
	ThreadCapabilities,
	ThreadReadResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import {
	createDurableSubmitter,
	createNativeMutationHost,
} from "./nativeMutationHost";
import {
	NativeMutationRuntime,
	nativeMutationTargetKey,
	type NativeMutationRuntimeOptions,
} from "./nativeMutationRuntime";
import type { SqliteSync } from "./sqliteSync";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

const ALL_TRUE_CAPS: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: true,
	queue: true,
	goal: true,
	rename: true,
};

function makeThread(over: Partial<Thread> = {}): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "hello",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1_000_000,
		updatedAt: 1_000_000,
		status: { type: "idle" },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [],
		evener: {
			ref: "ref-1",
			capabilities: ALL_TRUE_CAPS,
			queue: { revision: 0, depth: 0, preview: [] },
		},
		...over,
	};
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

let openDatabases: SqliteDoubleDatabase[] = [];

afterEach(() => {
	for (const database of openDatabases) database.close();
	openDatabases = [];
});

function openDatabase(): SqliteSync {
	const opened = openSqliteSyncDouble();
	openDatabases.push(opened.database);
	return opened.port;
}

// The production composition the screen mounts: one runtime, one host binding
// the screen's client and target, a service fenced through the host's read
// hooks, and a store whose mutations are admitted through the runtime.
function compose(runtimeOptions: NativeMutationRuntimeOptions = {}) {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: (() => {
			let next = 0;
			return () => `mutation-${++next}`;
		})(),
		now: () => 1,
		getOwnClientId: () => "origin-a",
		...runtimeOptions,
	});
	const client = new FakeClient("ready");
	const host = createNativeMutationHost(runtime, "hub-1", "ref-1", client);
	const service = createConversationService(client, {
		onReadStart: (ref, expectedThreadId) =>
			host.beginRead(ref, expectedThreadId),
		onReadComplete: (lease, response) =>
			host.reconcileRead(lease, response),
	});
	const store = createConversationStore({
		mutationHubId: "hub-1",
		mutationSubmitter: runtime,
	});
	return {
		runtime,
		client,
		host,
		service,
		store,
		targetKey: nativeMutationTargetKey("hub-1", "ref-1"),
	};
}

async function open(host: ReturnType<typeof createNativeMutationHost>, client: FakeClient, store: ReturnType<typeof createConversationStore>, service: ReturnType<typeof createConversationService>) {
	client.on("thread/read", () => ({ thread: makeThread() }) as ThreadReadResponse);
	await host.start();
	await store
		.getState()
		.openProjected(service, createActivityStore().getState(), "ref-1");
}

test("a rejected production send is admitted durably and lands a recovery row the panel surfaces", async () => {
	const { runtime, client, host, service, store, targetKey } = compose();
	client.on("turn/start", (params) => {
		const { clientMutationId } = params as { clientMutationId: string };
		throw new WireError("turn is not active", -32000, {
			clientMutationId,
			mutationOutcome: "notAccepted",
		});
	});
	await open(host, client, store, service);
	store.getState().setDraft("my message");

	const previous = store.getState().lastAcceptedMutation;
	await store
		.getState()
		.send(service, [{ type: "text", text: "my message" }]);

	// The store reported durable admission without inventing a wire receipt,
	// and the service transport was never called for the mutation.
	expect(store.getState().lastAcceptedMutation).toEqual({
		kind: "send",
		receipt: undefined,
	});
	expect(store.getState().lastAcceptedMutation).not.toBe(previous);
	await vi.waitFor(() => {
		expect(
			client.calls.filter((call) => call.method === "turn/start"),
		).toHaveLength(1);
	});

	await vi.waitFor(async () => {
		const snapshot = await runtime.read(targetKey);
		expect(snapshot.recovery).toMatchObject([
			{ method: "turn/start", recoveryKind: "rejected" },
		]);
		expect(snapshot.outbox).toEqual([]);
	});
	await runtime.stop();
});

test("a durable send clears the composer's durable unconfirmed draft at enqueue, not at settlement", async () => {
	const { runtime, client, host, service, store, targetKey } = compose();
	let releaseTurn!: (value: unknown) => void;
	client.on(
		"turn/start",
		() =>
			new Promise((resolve) => {
				releaseTurn = resolve;
			}) as never,
	);

	const openedDraft = openSqliteSyncDouble();
	openDatabases.push(openedDraft.database);
	const repository = new DraftRepository(openedDraft.port);
	const document = new DraftDocument(() => repository, {
		hubId: "hub-1",
		sessionRef: "ref-1",
	});
	document.edit("my message");

	await open(host, client, store, service);

	// The production operation exactly as the screen builds it.
	await document.submit(async (text) => {
		store.getState().setDraft(text);
		const previous = store.getState().lastAcceptedMutation;
		await store.getState().send(service, [{ type: "text", text }]);
		const accepted = store.getState().lastAcceptedMutation;
		return accepted != null && accepted !== previous && accepted.kind === "send";
	});

	// The intent is durably enqueued and the wire turn is still in flight...
	await vi.waitFor(() => {
		expect(
			client.calls.filter((call) => call.method === "turn/start"),
		).toHaveLength(1);
	});
	expect((await runtime.read(targetKey)).outbox).toMatchObject([
		{ state: "submitting" },
	]);
	// ...yet the unconfirmed draft marker is already gone: a crash here cannot
	// resurrect the message the outbox owns.
	expect(document.getSnapshot().record.unconfirmed).toBeNull();
	expect(document.getSnapshot().record.draft).toBe("");

	releaseTurn({
		receipt: {
			clientMutationId: "mutation-1",
			disposition: "applied",
			threadId: "thread-1",
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	});
	await runtime.stop();
});

test("a failed host startup keeps the registration so a later admission still dispatches", async () => {
	// The runtime's first start attempt fails (an unavailable timer port); the
	// host registration must survive it, or the next admission would be
	// durably enqueued with no client bound and never dispatched.
	let attempts = 0;
	const { runtime, client, host, service, store } = compose({
		setInterval: (callback) => {
			attempts += 1;
			if (attempts === 1) throw new Error("timer setup unavailable");
			return 1;
		},
		clearInterval: () => undefined,
	});
	client.on("thread/read", () => ({ thread: makeThread() }) as ThreadReadResponse);
	client.on("turn/start", appliedReceipt);

	await expect(host.start()).rejects.toThrow("timer setup unavailable");
	// The registration survived: the read fence still hands out a lease, so the
	// retried start has a bound client to dispatch for.
	expect(host.beginRead("ref-1")).toBeDefined();

	await store
		.getState()
		.openProjected(service, createActivityStore().getState(), "ref-1");
	store.getState().setDraft("retry after failure");
	await store
		.getState()
		.send(service, [{ type: "text", text: "retry after failure" }]);

	await vi.waitFor(() => {
		expect(
			client.calls.filter((call) => call.method === "turn/start"),
		).toHaveLength(1);
	});
	await runtime.stop();
});

test("a synchronous registration failure rejects the start promise instead of throwing", async () => {
	// A client the runtime cannot bind (no onStateChange) makes registerTarget
	// throw synchronously. start() must surface that as a rejected promise, not
	// throw out of the caller's effect and crash the conversation screen.
	const opened = openSqliteSyncDouble();
	openDatabases.push(opened.database);
	const runtime = new NativeMutationRuntime(opened.port, {
		createMutationId: () => "mutation-1",
		now: () => 1,
	});
	const unboundClient = { state: "ready" } as unknown as AppwireClientLike;
	const host = createNativeMutationHost(runtime, "hub-1", "ref-1", unboundClient);

	let started!: Promise<void>;
	expect(() => {
		started = host.start();
	}).not.toThrow();
	await expect(started).rejects.toThrow();
});

test("the durable submitter refuses while no host is live and delegates once one is", async () => {
	const request = {
		kind: "send" as const,
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "thread-1",
		input: [{ type: "text" as const, text: "x" }],
	};
	// No host: a submission must be refused, not durably accepted, because
	// nothing would dispatch it.
	const refused = createDurableSubmitter(() => null);
	await expect(refused.submit(request)).rejects.toThrow(
		"Durable sending is unavailable",
	);

	// A live host delegates to the runtime and enqueues durably.
	const { runtime, host, targetKey } = compose();
	await host.start();
	const live = createDurableSubmitter(() => host);
	await live.submit(request);
	expect((await runtime.read(targetKey)).outbox).toHaveLength(1);
	await runtime.stop();
});
