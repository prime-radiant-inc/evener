import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test, vi } from "vitest";
import type { Thread, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createNativeMutationHost } from "./nativeMutationHost";
import {
	NativeMutationRuntime,
	type NativeMutationRequest,
} from "./nativeMutationRuntime";
import type { MutationOutboxDatabase } from "./mutationOutboxStorage";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, resolve, reject };
}

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

const capabilities = {
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

function nativeThread(ref: string, id: string): Thread {
	return {
		id,
		sessionId: id,
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
			ref,
			capabilities,
			queue: { revision: 0, clientMutationIds: [] },
			mutationStateAuthoritative: true,
		},
	};
}

function nativeReadResponse(ref: string, id: string): ThreadReadResponse {
	return { thread: nativeThread(ref, id) };
}

function runtimeFixture() {
	const leases = [{ targetKey: "key-1" }, { targetKey: "key-2" }];
	const runtime = {
		registerTarget: vi.fn(() => vi.fn()),
		start: vi.fn(async () => undefined),
		beginAuthoritativeRead: vi.fn(() => leases[0]),
		reconcileAuthoritativeRead: vi.fn(async () => "reconciled" as const),
	};
	return { runtime, leases };
}

test("construction does not register a target before the committed start", async () => {
	const { runtime } = runtimeFixture();
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	expect(runtime.registerTarget).not.toHaveBeenCalled();
	expect(host.beginRead("ref-1")).toBeUndefined();
	await host.start();
	expect(runtime.registerTarget).toHaveBeenCalledTimes(1);
});

test("re-registers after a committed effect is disposed and restarted", async () => {
	const { runtime, leases } = runtimeFixture();
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	await host.start();
	host.dispose();
	await host.start();

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});

test("retries registration after the current startup rejects", async () => {
	const { runtime, leases } = runtimeFixture();
	runtime.start
		.mockRejectedValueOnce(new Error("startup failed"))
		.mockResolvedValueOnce(undefined);
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	await expect(host.start()).rejects.toThrow("startup failed");
	await host.start();

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});

test("a stale startup rejection cannot unregister a restarted owner", async () => {
	const firstStart = deferred<undefined>();
	const { runtime, leases } = runtimeFixture();
	runtime.start
		.mockImplementationOnce(() => firstStart.promise)
		.mockImplementationOnce(async () => undefined);
	const host = createNativeMutationHost(
		runtime as never,
		"hub-1",
		"ref-1",
		{ state: "ready" } as never,
	);

	const first = host.start();
	host.dispose();
	const restarted = host.start();
	await restarted;
	firstStart.reject(new Error("first startup failed"));
	await expect(first).rejects.toThrow("first startup failed");

	expect(runtime.registerTarget).toHaveBeenCalledTimes(2);
	expect(host.beginRead("ref-1")).toBe(leases[0]);
});

test("reconciles and dispatches a replacement ref with its own native thread", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-b",
	});
	const client = new FakeClient("ready");
	const hostA = createNativeMutationHost(runtime, "hub-1", "ref-a", client);
	const hostB = createNativeMutationHost(runtime, "hub-1", "ref-b", client);
	client.on("thread/read", (params) => {
		const ref = (params as { ref: string }).ref;
		return nativeReadResponse(ref, ref === "ref-a" ? "thread-a" : "thread-b");
	});
	client.on("turn/start", (params) => ({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: "thread-b",
			turnId: "turn-b",
			projectionState: "pending",
		},
		turn: { id: "turn-b" },
	}) as never);
	await hostA.start();
	await hostB.start();
	const service = createConversationService(client, {
		onReadStart: (ref, expectedThreadId) =>
			(ref === "ref-a" ? hostA : hostB).beginRead(ref, expectedThreadId),
		onReadComplete: (lease, response) =>
			(lease?.targetRef === "ref-a" ? hostA : hostB).reconcileRead(
				lease,
				response,
			),
	});

	await service.open("ref-a");
	await runtime.submit({
		hubId: "hub-1",
		targetRef: "ref-b",
		threadId: "thread-b",
		instanceId: "instance-b",
		kind: "send",
		input: [{ type: "text", text: "hello" }],
	} satisfies NativeMutationRequest);
	expect(client.calls.some((call) => call.method === "turn/start")).toBe(false);
	await service.open("ref-b");

	await vi.waitFor(() =>
		expect(client.calls.some((call) => call.method === "turn/start")).toBe(true),
	);
	await runtime.stop();
	hostA.dispose();
	hostB.dispose();
});
