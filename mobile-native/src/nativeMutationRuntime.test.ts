import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { MutationOutboxSQLite, type MutationOutboxDatabase } from "./mutationOutboxStorage";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
	NativeMutationRuntime,
	type NativeMutationRequest,
} from "./nativeMutationRuntime";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

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

function request(kind: NativeMutationRequest["kind"]): NativeMutationRequest {
	return {
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		kind,
		input: [{ type: "text", text: "hello" }],
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

test("submit durably records while disconnected and dispatches after readiness", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("closed");
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();

	await runtime.submit(request("send"));
	const queued = await runtime.storage.getOutbox("mutation-1");
	expect(queued?.state).toBe("submitting");
	expect(client.calls).toHaveLength(0);

	const delivered = new Promise<void>((resolve) => {
		client.on("turn/start", (params) => {
			resolve();
			return appliedReceipt(params);
		});
	});
	client.emitStateChange("ready");
	await delivered;
	await runtime.connectionReady();

	expect(client.calls).toHaveLength(1);
	expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
});

test("cleanup of an old registration cannot remove a replacement for the same target", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const firstClient = new FakeClient("closed");
	const replacementClient = new FakeClient("closed");
	const unregisterFirst = runtime.registerTarget("hub-1", "ref-1", firstClient);
	const unregisterReplacement = runtime.registerTarget(
		"hub-1",
		"ref-1",
		replacementClient,
	);
	runtime.registerTarget("hub-1", "ref-1", null);
	unregisterFirst();

	const delivered = new Promise<void>((resolve) => {
		replacementClient.on("turn/start", (params) => {
			resolve();
			return appliedReceipt(params);
		});
	});
	await runtime.submit(request("send"));
	firstClient.emitStateChange("ready");
	replacementClient.emitStateChange("ready");
	await delivered;

	expect(firstClient.calls).toHaveLength(0);
	expect(replacementClient.calls).toHaveLength(1);
	unregisterReplacement();
});

test("same raw ref stays isolated by hub across dispatch and restart", async () => {
	let nextId = 0;
	const sharedDatabase = openDatabase();
	const runtime = new NativeMutationRuntime(sharedDatabase, {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const firstClient = new FakeClient("closed");
	const secondClient = new FakeClient("closed");
	runtime.registerTarget("hub-a", "shared-ref", firstClient);
	runtime.registerTarget("hub-b", "shared-ref", secondClient);

	await runtime.submit({ ...request("send"), hubId: "hub-a", targetRef: "shared-ref" });
	await runtime.submit({ ...request("send"), hubId: "hub-b", targetRef: "shared-ref" });
	expect(await runtime.storage.listTargetRefs()).toEqual([
		nativeMutationTargetKey("hub-a", "shared-ref"),
		nativeMutationTargetKey("hub-b", "shared-ref"),
	]);

	const firstDelivered = new Promise<void>((resolve) => {
		firstClient.on("turn/start", (params) => {
			resolve();
			return appliedReceipt(params);
		});
	});
	firstClient.emitStateChange("ready");
	await firstDelivered;
	expect(firstClient.calls[0]?.params).toMatchObject({ ref: "shared-ref" });
	expect(secondClient.calls).toHaveLength(0);
	await runtime.stop();

	const secondDelivered = new Promise<void>((resolve) => {
		secondClient.on("turn/start", (params) => {
			resolve();
			return appliedReceipt(params);
		});
	});
	const restarted = new NativeMutationRuntime(sharedDatabase, {
		createMutationId: () => `restart-${++nextId}`,
	});
	restarted.registerTarget("hub-b", "shared-ref", secondClient);
	await restarted.start();
	secondClient.emitStateChange("ready");
	await secondDelivered;
	expect(secondClient.calls[0]?.params).toMatchObject({ ref: "shared-ref" });
	await restarted.stop();
});

test("submit resolves at the durable enqueue boundary", async () => {
	let release!: (value: unknown) => void;
	const pendingResponse = new Promise<unknown>((resolve) => {
		release = resolve;
	});
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", () => pendingResponse as never);
	runtime.registerTarget("hub-1", "ref-1", client);

	const submission = runtime.submit(request("send"));
	const boundary = await Promise.race([
		submission.then(() => "enqueued" as const),
		new Promise<"waiting">((resolve) => setTimeout(() => resolve("waiting"), 100)),
	]);
	release({
		receipt: {
			clientMutationId: "mutation-1",
			disposition: "applied",
			threadId: "thread-1",
			turnId: "turn-1",
			projectionState: "pending",
		},
		turn: { id: "turn-1" },
	} as never);
	await submission;
	await runtime.stop();

	expect(boundary).toBe("enqueued");
});

test("each enqueue gets a fresh mutation id", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	await runtime.submit(request("queue"));
	await runtime.submit(request("queue"));

	const first = await runtime.storage.getOutbox("mutation-1");
	const second = await runtime.storage.getOutbox("mutation-2");
	expect(first?.intentSequence).toBe(1);
	expect(second?.intentSequence).toBe(2);
});

test("steer with a queue revision uses the drain route and preserves its fence", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	await runtime.submit({ ...request("steer"), expectedQueueRevision: 7 });

	const queued = await runtime.storage.getOutbox("mutation-1");
	expect(queued).toMatchObject({
		method: "turn/drainAsSteer",
		payload: {
			ref: "ref-1",
			expectedInstanceId: "instance-1",
			expectedQueueRevision: 7,
		},
	});
});

test("the process getter reuses one runtime and database handle across provider lifetimes", async () => {
	const injectedDatabase = openDatabase();
	expoSQLite.openDatabaseSync.mockReturnValue(injectedDatabase as never);

	const first = getNativeMutationRuntime();
	const second = getNativeMutationRuntime();

	expect(second).toBe(first);
	expect(expoSQLite.openDatabaseSync).toHaveBeenCalledTimes(1);
	await first.start();
	await first.stop();
	await first.start();
	await first.stop();
});
