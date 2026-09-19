import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { createConversationService } from "../../mobile/src/services/conversation";
import { MutationOutboxSQLite, type MutationOutboxDatabase } from "./mutationOutboxStorage";
import { NativeMutationRuntime } from "./nativeMutationRuntime";
import { createNativeMutationHost } from "./nativeMutationHost";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({
	 randomUUID: () => "test-uuid",
	 getRandomValues: (array: Uint8Array) => array,
}));

let database: DatabaseSync | undefined;

function openDatabase(): MutationOutboxDatabase {
	database = new DatabaseSync(":memory:");
	return {
		execSync: (sql) => database?.exec(sql),
		runSync: (sql, ...params) => database?.prepare(sql).run(...params) ?? { changes: 0 },
		getFirstSync: <T>(sql: string, ...params: (string | number)[]) =>
			(database?.prepare(sql).get(...params) as T | undefined) ?? null,
		getAllSync: <T>(sql: string, ...params: (string | number)[]) =>
			(database?.prepare(sql).all(...params) as T[]) ?? [],
	};
}

afterEach(() => {
	database?.close();
	database = undefined;
});

const capabilities: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: false,
	queue: true,
	goal: true,
	rename: true,
};

function readResponse(): ThreadReadResponse {
	const thread: Thread = {
		id: "thread-1",
		sessionId: "session-1",
		preview: "hello",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1,
		updatedAt: 1,
		status: { type: "ready" },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [],
		evener: {
			ref: "ref-1",
			capabilities,
			queue: { revision: 0, clientMutationIds: [] },
			mutationStateAuthoritative: true,
		},
	};
	return { thread };
}

test("captures the lease before a raw read and dispatches after reconciliation", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", (params) => ({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: "thread-1",
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	}) as never);
	let releaseRead!: (response: ThreadReadResponse) => void;
	client.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => {
		releaseRead = resolve;
	}));
	const host = createNativeMutationHost(runtime, "hub-1", "ref-1", client);
	expect(host.beginRead("ref-1")).toBeUndefined();
	await host.start();
	await runtime.submit({
		kind: "send",
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text", text: "hello" }],
	});

	const service = createConversationService(client, {
		onReadStart: (ref, expectedThreadId) => host.beginRead(ref, expectedThreadId),
		onReadComplete: (lease, response) => host.reconcileRead(lease, response),
	});
	const read = service.readProjection("ref-1");
	expect(client.calls).toHaveLength(1);
	expect(client.calls[0]?.method).toBe("thread/read");
	expect(client.calls.filter(({ method }) => method === "turn/start")).toHaveLength(0);

	await Promise.resolve();
	releaseRead(readResponse());
	await read;
	await vi.waitFor(() => expect(client.calls.filter(({ method }) => method === "turn/start")).toHaveLength(1));
	await host.stop();
});

test("disposing the host invalidates a read lease before its response arrives", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", () => ({}) as never);
	let releaseRead!: (response: ThreadReadResponse) => void;
	client.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => {
		releaseRead = resolve;
	}));
	const host = createNativeMutationHost(runtime, "hub-1", "ref-1", client);
	await host.start();
	await runtime.submit({
		kind: "send",
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text", text: "hello" }],
	});
	const service = createConversationService(client, {
		onReadStart: (ref, expectedThreadId) => host.beginRead(ref, expectedThreadId),
		onReadComplete: (lease, response) => host.reconcileRead(lease, response),
	});
	const read = service.readProjection("ref-1");
	await Promise.resolve();
	host.dispose();
	releaseRead(readResponse());
	await read;
	expect(client.calls.filter(({ method }) => method === "turn/start")).toHaveLength(0);
	await host.stop();
});

test("cleans up registration when runtime startup rejects", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	const host = createNativeMutationHost(runtime, "hub-1", "ref-1", client);
	const start = vi
		.spyOn(runtime, "start")
		.mockRejectedValue(new Error("database unavailable"));

	await expect(host.start()).rejects.toThrow("database unavailable");
	expect(host.beginRead("ref-1")).toBeUndefined();
	start.mockRestore();
	await host.stop();
});
