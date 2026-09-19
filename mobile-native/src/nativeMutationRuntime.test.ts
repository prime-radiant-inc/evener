import { DatabaseSync } from "node:sqlite";
import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ThreadReadResponse } from "@evener/appwire-client";
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

function readResponse(
	targetRef: string,
	options: {
		authoritative?: boolean;
		ids?: string[];
		resumeRequired?: boolean;
		status?: string;
		threadId?: string;
	} = {},
): ThreadReadResponse {
	return {
		thread: {
			id: options.threadId ?? "thread-1",
			status: { type: options.status ?? "ready" },
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

function registerAndStart(runtime: NativeMutationRuntime, client: FakeClient, hubId = "hub-1", targetRef = "ref-1") {
	runtime.registerTarget(hubId, targetRef, client);
	return runtime.start();
}

test("a target stays gated until a matching authoritative read opens it", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);

	await runtime.submit(request("send"));
	expect(client.calls).toHaveLength(0);

	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	expect(lease).toBeDefined();
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));

	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	await runtime.stop();
});

test.each([
	["non-authoritative", { authoritative: false }],
	["not-loaded", { status: "notLoaded" }],
] as const)("never-attempted work remains dispatchable after a %s read", async (_name, options) => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));

	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1", options));

	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	await runtime.stop();
});

test("a non-authoritative read blocks attempted work without blocking later targets", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: (() => {
			let next = 0;
			return () => `mutation-${++next}`;
		})(),
	});
	const firstClient = new FakeClient("ready");
	const secondClient = new FakeClient("ready");
	firstClient.on("turn/start", appliedReceipt);
	secondClient.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, firstClient, "hub-a", "ref-a");
	runtime.registerTarget("hub-b", "ref-b", secondClient);

	await runtime.submit({ ...request("send"), hubId: "hub-a", targetRef: "ref-a" });
	await runtime.storage.markAttempted("mutation-1");
	const firstLease = runtime.beginAuthoritativeRead("hub-a", "ref-a", firstClient);
	await runtime.reconcileAuthoritativeRead(
		firstLease!,
		readResponse("ref-a", { authoritative: false }),
	);

	await runtime.submit({ ...request("send"), hubId: "hub-b", targetRef: "ref-b" });
	const secondLease = runtime.beginAuthoritativeRead("hub-b", "ref-b", secondClient);
	await runtime.reconcileAuthoritativeRead(secondLease!, readResponse("ref-b"));

	await vi.waitFor(() => expect(secondClient.calls).toHaveLength(1));
	expect(firstClient.calls).toHaveLength(0);
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "blockedUnknown" });
	await runtime.stop();
});

test("an overlapping read from the same client cannot reopen an older lease", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));

	const oldLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	const currentLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(oldLease!, readResponse("ref-1"));
	expect(client.calls).toHaveLength(0);
	await runtime.reconcileAuthoritativeRead(currentLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	await runtime.stop();
});

test("a replaced client and a mismatched raw ref cannot clear the target gate", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const oldClient = new FakeClient("ready");
	const newClient = new FakeClient("ready");
	newClient.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, oldClient);
	await runtime.submit(request("send"));
	const oldLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", oldClient);
	runtime.registerTarget("hub-1", "ref-1", newClient);
	await runtime.reconcileAuthoritativeRead(oldLease!, readResponse("ref-1"));
	expect(newClient.calls).toHaveLength(0);

	const newLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", newClient);
	await runtime.reconcileAuthoritativeRead(newLease!, readResponse("wrong-ref"));
	expect(newClient.calls).toHaveLength(0);
	await runtime.reconcileAuthoritativeRead(newLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(newClient.calls).toHaveLength(1));
	await runtime.stop();
});

test("resume-required reads hold even never-attempted work until a later valid read", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));

	const firstLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(
		firstLease!,
		readResponse("ref-1", { resumeRequired: true }),
	);
	expect(client.calls).toHaveLength(0);

	const secondLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(secondLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	await runtime.stop();
});

test("a reconnect re-gates a target until its next authoritative read", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));
	const firstLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(firstLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));

	client.emitStateChange("reconnecting");
	await runtime.submit(request("send"));
	client.emitStateChange("ready");
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));

	const secondLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(secondLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(2));
	await runtime.stop();
});

test("a blocked outcome invalidates an older pending read lease", async () => {
	let releaseResponse!: (error: unknown) => void;
	let requestStarted!: () => void;
	const pendingResponse = new Promise<never>((_resolve, reject) => {
		releaseResponse = reject;
	});
	const started = new Promise<void>((resolve) => {
		requestStarted = resolve;
	});
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", (params) => {
		requestStarted();
		return pendingResponse as never;
	});
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));
	const initialLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(initialLease!, readResponse("ref-1"));
	await started;

	const olderLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	releaseResponse(
		new WireError("journal unavailable", -32014, {
			evenerErrorInfo: "mutationOutcomeUnknown",
			clientMutationId: "mutation-1",
			mutationOutcome: "unknown",
			retryDisposition: "blocked",
			cause: "persistenceUnavailable",
		}),
	);
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "blockedUnknown" });
	});

	expect(await runtime.reconcileAuthoritativeRead(olderLease!, readResponse("ref-1"))).toBe("stale");
	await runtime.connectionReady();
	expect(client.calls).toHaveLength(1);
	await runtime.stop();
});

test("a failed blocked-outcome write keeps retry gated until a later read", async () => {
	const database = openDatabase();
	const originalRunSync = database.runSync;
	let failMarkUnknown = false;
	database.runSync = (sql, ...params) => {
		if (failMarkUnknown && sql.includes("UPDATE mutation_outbox SET state = ?") && params[0] === "blockedUnknown")
			throw new Error("journal unavailable");
		return originalRunSync(sql, ...params);
	};
	const intervals: Array<() => void> = [];
	let releaseResponse!: (error: unknown) => void;
	let requestStarted!: () => void;
	const pendingResponse = new Promise<never>((_resolve, reject) => {
		releaseResponse = reject;
	});
	const started = new Promise<void>((resolve) => {
		requestStarted = resolve;
	});
	let calls = 0;
	const runtime = new NativeMutationRuntime(database, {
		createMutationId: () => "mutation-1",
		setInterval: (callback) => {
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: () => undefined,
	});
	const client = new FakeClient("ready");
	client.on("turn/start", (params) => {
		calls += 1;
		if (calls === 1) {
			requestStarted();
			return pendingResponse as never;
		}
		return appliedReceipt(params);
	});
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));
	const initialLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(initialLease!, readResponse("ref-1"));
	await started;

	failMarkUnknown = true;
	releaseResponse(
		new WireError("journal unavailable", -32014, {
			evenerErrorInfo: "mutationOutcomeUnknown",
			clientMutationId: "mutation-1",
			mutationOutcome: "unknown",
			retryDisposition: "blocked",
			cause: "persistenceUnavailable",
		}),
	);
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({
			state: "submitting",
			attempted: true,
		});
	});

	intervals[0]?.();
	await runtime.connectionReady();
	expect(calls).toBe(1);

	const retryLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(retryLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(calls).toBe(2));
	expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	await runtime.stop();
});

test("confirmed identities settle before absent blocked identities are restored", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));
	await runtime.submit(request("send"));
	for (const id of ["mutation-1", "mutation-2"]) {
		await runtime.storage.markAttempted(id);
		await runtime.storage.markUnknown(id, "blockedUnknown", { onlyAttempted: true });
	}

	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(
		lease!,
		readResponse("ref-1", { ids: ["mutation-1"] }),
	);

	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	expect(client.calls[0]?.params).toMatchObject({ clientMutationId: "mutation-2" });
	expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	await runtime.stop();
});

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
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
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
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", replacementClient);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
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
	const firstLease = runtime.beginAuthoritativeRead("hub-a", "shared-ref", firstClient);
	await runtime.reconcileAuthoritativeRead(firstLease!, readResponse("shared-ref"));
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
	const secondLease = restarted.beginAuthoritativeRead("hub-b", "shared-ref", secondClient);
	await restarted.reconcileAuthoritativeRead(secondLease!, readResponse("shared-ref"));
	await secondDelivered;
	expect(secondClient.calls[0]?.params).toMatchObject({ ref: "shared-ref" });
	await restarted.stop();
});

test("a ready target dispatches while another target remains unresolved", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const firstClient = new FakeClient("closed");
	const secondClient = new FakeClient("closed");
	runtime.registerTarget("hub-a", "ref-a", firstClient);
	runtime.registerTarget("hub-b", "ref-b", secondClient);
	await runtime.start();

	let releaseFirst!: () => void;
	const firstStarted = new Promise<void>((resolve) => {
		firstClient.on("turn/start", (params) => {
			resolve();
			return new Promise((resolveResponse) => {
				releaseFirst = () => resolveResponse(appliedReceipt(params));
			});
		});
	});
	const secondDelivered = new Promise<void>((resolve) => {
		secondClient.on("turn/start", (params) => {
			resolve();
			return appliedReceipt(params);
		});
	});
	await runtime.submit({ ...request("send"), hubId: "hub-a", targetRef: "ref-a" });
	await runtime.submit({ ...request("send"), hubId: "hub-b", targetRef: "ref-b" });

	firstClient.emitStateChange("ready");
	const firstLease = runtime.beginAuthoritativeRead("hub-a", "ref-a", firstClient);
	await runtime.reconcileAuthoritativeRead(firstLease!, readResponse("ref-a"));
	await firstStarted;
	secondClient.emitStateChange("ready");
	const secondLease = runtime.beginAuthoritativeRead("hub-b", "ref-b", secondClient);
	await runtime.reconcileAuthoritativeRead(secondLease!, readResponse("ref-b"));
	await secondDelivered;
	expect(secondClient.calls).toHaveLength(1);

	releaseFirst();
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
		expect(await runtime.storage.getOutbox("mutation-2")).toBeUndefined();
	});
	await runtime.stop();
});

test("stop fences the next queued send while an in-flight request settles", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("closed");
	runtime.registerTarget("hub-1", "ref-1", client);
	let releaseFirst!: () => void;
	const firstStarted = new Promise<void>((resolve) => {
		client.on("turn/start", (params) => {
			resolve();
			return new Promise((resolveResponse) => {
				releaseFirst = () => resolveResponse(appliedReceipt(params));
			});
		});
	});
	let releaseSecond!: () => void;
	let resolveSecondStarted!: () => void;
	const secondStarted = new Promise<void>((resolve) => {
		resolveSecondStarted = resolve;
	});
	let secondAttempts = 0;
	client.on("turn/queue", (params) => {
		secondAttempts += 1;
		if (secondAttempts === 1) {
			resolveSecondStarted();
			return new Promise((resolveResponse) => {
				releaseSecond = () => resolveResponse(appliedReceipt(params));
			});
		}
		return appliedReceipt(params);
	});
	await runtime.start();
	await runtime.submit(request("send"));
	await runtime.submit(request("queue"));
	client.emitStateChange("ready");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await firstStarted;
	await runtime.stop();

	releaseFirst();
	await vi.waitFor(async () => {
		 expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	});
	expect(client.calls).toHaveLength(1);
	expect(await runtime.storage.getOutbox("mutation-2")).toMatchObject({ state: "submitting" });

	await runtime.start();
	await secondStarted;
	expect(client.calls).toHaveLength(2);
	releaseSecond();
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-2")).toBeUndefined();
	});
	expect(secondAttempts).toBe(1);
	await runtime.stop();
});

test("an interval retries a ready transport failure with the same mutation id", async () => {
	let nextId = 0;
	const intervals: Array<() => void> = [];
	const cleared: number[] = [];
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
		setInterval: (callback) => {
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: (intervalId) => cleared.push(intervalId),
	});
	const client = new FakeClient("closed");
	runtime.registerTarget("hub-1", "ref-1", client);
	const attempts: string[] = [];
	let firstAttempt!: () => void;
	let retryAttempt!: () => void;
	const firstAttemptStarted = new Promise<void>((resolve) => {
		firstAttempt = resolve;
	});
	const retryAttemptStarted = new Promise<void>((resolve) => {
		retryAttempt = resolve;
	});
	client.on("turn/start", (params) => {
		const { clientMutationId } = params as { clientMutationId: string };
		attempts.push(clientMutationId);
		if (attempts.length === 1) {
			firstAttempt();
			return Promise.reject(new Error("transport timeout")) as never;
		}
		retryAttempt();
		return appliedReceipt(params);
	});

	await runtime.start();
	expect(intervals).toHaveLength(1);
	await runtime.submit(request("send"));
	client.emitStateChange("ready");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await firstAttemptStarted;
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({
			state: "submitting",
			attempted: true,
		});
	});

	await vi.waitFor(() => {
		intervals[0]?.();
		expect(attempts).toHaveLength(2);
	});
	await retryAttemptStarted;
	expect(attempts).toEqual(["mutation-1", "mutation-1"]);
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	});

	await runtime.stop();
	expect(cleared).toEqual([1]);
	await runtime.start();
	expect(intervals).toHaveLength(2);
	await runtime.stop();
	expect(cleared).toEqual([1, 2]);
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
	let requestStarted!: () => void;
	const started = new Promise<void>((resolve) => {
		requestStarted = resolve;
	});
	client.on("turn/start", () => {
		requestStarted();
		return pendingResponse as never;
	});
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));

	const submission = runtime.submit(request("send"));
	await started;
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
