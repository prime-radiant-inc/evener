import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { MutationOutboxSQLite, type MutationOutboxDatabase } from "./mutationOutboxStorage";
import { NativeMutationRuntime, type NativeMutationRequest } from "./nativeMutationRuntime";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: () => { throw new Error("test database must be injected"); } }));
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
		service: {} as NativeMutationRequest["service"],
	};
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

	client.on("turn/start", (params) => {
		const payload = params as { clientMutationId: string };
		return {
			receipt: {
				clientMutationId: payload.clientMutationId,
				disposition: "applied",
				threadId: "thread-1",
				turnId: "turn-1",
				projectionState: "pending",
			},
			turn: { id: "turn-1" },
		} as never;
	});
	client.emitStateChange("ready");
	await runtime.connectionReady();

	expect(client.calls).toHaveLength(1);
	expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
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
