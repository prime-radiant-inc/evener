import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
	MutationOutbox,
	createMutationProjectionFence,
	type MutationAttachmentRef,
	type MutationLifecycleTarget,
	type MutationOutboxChannel,
	type MutationPersistencePort,
	type MutationPersistenceSnapshot,
} from "@evener/appwire-client/state/mutation";
import type { ThreadReadResponse } from "@evener/appwire-client";
import type { SqliteSync } from "./sqliteSync";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
	NativeMutationRuntime,
	type NativeMutationRequest,
} from "./nativeMutationRuntime";

const expoSQLite = vi.hoisted(() => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-sqlite", () => expoSQLite);
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

let database: SqliteDoubleDatabase | undefined;

function openDatabase(): SqliteSync {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	return opened.port;
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

// The startup/retry regressions share a real NativeMutationRuntime with an
// injected timer port that records the armed callbacks and cleared ids and can
// throw on chosen setup attempts; no real timer is ever created.
function timerRecordingRuntime(failSetups?: (attempt: number) => boolean) {
	const intervals: Array<() => void> = [];
	const cleared: number[] = [];
	let attempts = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
		setInterval: (callback) => {
			attempts += 1;
			if (failSetups?.(attempts)) throw new Error("timer setup unavailable");
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: (intervalId) => cleared.push(intervalId),
	});
	return { runtime, intervals, cleared, attempts: () => attempts };
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

test("read and storage subscriptions expose a scoped rejection without changing origin identity", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
		getOwnClientId: () => "origin-a",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", (params) => {
		const { clientMutationId } = params as { clientMutationId: string };
		throw new WireError("turn is not active", -32000, {
			clientMutationId,
			mutationOutcome: "notAccepted",
		});
	});
	runtime.registerTarget("hub-a", "ref-1", client);
	await runtime.start();
	const targetKey = nativeMutationTargetKey("hub-a", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => {
		changes.push([...targetRefs]);
	});

	await runtime.submit({ ...request("send"), hubId: "hub-a" });
	expect(changes).toEqual([[targetKey]]);
	const lease = runtime.beginAuthoritativeRead("hub-a", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await vi.waitFor(async () => {
		expect(await runtime.storage.getRecovery("mutation-1")).toMatchObject({
			targetRef: targetKey,
			originClientId: "origin-a",
			recoveryKind: "rejected",
		});
	});

	const snapshot = await runtime.read(targetKey);
	expect(snapshot.outbox).toEqual([]);
	expect(snapshot.optimistic).toEqual([]);
	expect(snapshot.recovery).toMatchObject([{ clientMutationId: "mutation-1", targetRef: targetKey }]);
	expect(changes).toEqual([[targetKey], [targetKey]]);

	const changeCount = changes.length;
	unsubscribe();
	await runtime.submit({ ...request("send"), hubId: "hub-a" });
	expect(changes).toHaveLength(changeCount);
	await runtime.stop();
});

test("discardRecovery notifies storage listeners after the durable deletion completes", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
		getOwnClientId: () => "origin-a",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", (params) => {
		const { clientMutationId } = params as { clientMutationId: string };
		throw new WireError("turn is not active", -32000, {
			clientMutationId,
			mutationOutcome: "notAccepted",
		});
	});
	runtime.registerTarget("hub-a", "ref-1", client);
	await runtime.start();
	const targetKey = nativeMutationTargetKey("hub-a", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));

	await runtime.submit({ ...request("send"), hubId: "hub-a" });
	const lease = runtime.beginAuthoritativeRead("hub-a", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await vi.waitFor(async () => {
		expect(await runtime.storage.getRecovery("mutation-1")).toMatchObject({
			targetRef: targetKey,
			recoveryKind: "rejected",
		});
	});
	expect(changes).toEqual([[targetKey], [targetKey]]);
	// A second listener probes the durable state at notify time, so the
	// test can pin that it ran after the storage write: a notify that
	// raced ahead of the DELETE would still observe the row here.
	let readAtNotify: ReturnType<typeof runtime.read> | undefined;
	const unsubscribeProbe = runtime.subscribeStorage(() => {
		readAtNotify = runtime.read(targetKey);
	});

	const discarded = await runtime.discardRecovery("mutation-1", targetKey);
	expect(discarded).toBe(true);
	expect(changes).toEqual([[targetKey], [targetKey], [targetKey]]);
	expect(await runtime.storage.getRecovery("mutation-1")).toBeUndefined();
	expect(readAtNotify).toBeDefined();
	expect((await readAtNotify)?.recovery).toEqual([]);
	unsubscribe();
	unsubscribeProbe();
	await runtime.stop();
});

test("a discard that removes nothing still notifies the storage projection", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const targetKey = nativeMutationTargetKey("hub-a", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));

	// The zero-included rule (docs/design/stop-cancellation-outbox.md §4):
	// the notify follows the write's completion, not its rows-affected
	// count, so a discard whose DELETE commits over zero rows - the row was
	// already removed, or belongs to another target - still refreshes the
	// projection instead of waiting for an unrelated mutation event.
	const discarded = await runtime.discardRecovery("mutation-404", targetKey);
	expect(discarded).toBe(false);
	expect(changes).toEqual([[targetKey]]);
	unsubscribe();
});

test("a failed discard write stays silent and reports the failure", async () => {
	const database = openDatabase();
	const originalRunSync = database.runSync;
	database.runSync = (sql, ...params) => {
		if (sql.includes("DELETE FROM mutation_recovery")) throw new Error("journal unavailable");
		return originalRunSync(sql, ...params);
	};
	const runtime = new NativeMutationRuntime(database, {
		createMutationId: () => "mutation-1",
	});
	const targetKey = nativeMutationTargetKey("hub-a", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));

	// The other half of the zero-included rule: the notify follows a
	// completed write, so a failed one stays silent (§6: a failed discard
	// leaves the rows for the next attempt) and the error propagates.
	await expect(runtime.discardRecovery("mutation-1", targetKey)).rejects.toThrow("journal unavailable");
	expect(changes).toEqual([]);
	unsubscribe();
});

test("the runtime feeds the shared projection fence through its scoped read contract", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	await runtime.submit(request("queue"));

	const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
	// Compile-level conformance with no adapter: the scoped read returns
	// the package's own MutationPersistenceSnapshot, so a field added to
	// the shared snapshot breaks here instead of silently diverging in a
	// parallel native copy - and a read that takes the target key as
	// required still satisfies the fence's MutationPersistencePort
	// parameter structurally, because a required-arg method widens to the
	// port's optional-arg method shape.
	const fence = createMutationProjectionFence<MutationAttachmentRef>();
	const refresh = await fence.refresh(runtime, targetKey);
	if (!refresh) throw new Error("the scoped refresh must produce a snapshot");
	expect([...refresh.apply()]).toEqual([targetKey]);
	expect(refresh.snapshot.outbox).toMatchObject([
		{ clientMutationId: "mutation-1", targetRef: targetKey },
	]);
	expect(refresh.snapshot.optimistic).toEqual([]);
	expect(refresh.snapshot.recovery).toEqual([]);

	// The contract's own compile-level pin: the native read takes the
	// composite target key as REQUIRED, so the all-targets form the shared
	// port permits is not callable on the runtime's own type. The binding
	// is type-level only and never executes.
	// @ts-expect-error the native read contract requires the composite target key
	const readWithoutTarget: () => Promise<MutationPersistenceSnapshot<MutationAttachmentRef>> = runtime.read;
	expect(readWithoutTarget).toBe(runtime.read);

	// The one route that remains to the all-targets form is the structural
	// widening the port's method shape permits. The native recovery
	// projection is scoped (the storage's listRecovery requires the exact
	// composite key), so that form has no native backing: the widened
	// reference fails loudly rather than returning a snapshot that
	// silently drops recovery rows, and the fence degrades the rejected
	// read to a no-op refresh.
	const widened: MutationPersistencePort<MutationAttachmentRef> = runtime;
	await expect(widened.read()).rejects.toThrow(/targetRef is required/);
	expect(await fence.refresh(widened)).toBe(false);
});

test("an attempted non-authoritative read notifies the storage projection after blocking a record", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();
	const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));
	await runtime.submit(request("send"));
	await runtime.storage.markAttempted("mutation-1");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);

	await expect(runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1", { authoritative: false }))).resolves.toBe(
		"reconciled",
	);
	expect(changes).toEqual([[targetKey], [targetKey]]);
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "blockedUnknown" });
	unsubscribe();
	await runtime.stop();
});

test("a stale lease still notifies after its non-authoritative blocking write commits", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	const replacement = new FakeClient("ready");
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();
	const targetKey = nativeMutationTargetKey("hub-1", "ref-1");
	const changes: string[][] = [];
	const unsubscribe = runtime.subscribeStorage((targetRefs) => changes.push([...targetRefs]));
	await runtime.submit(request("send"));
	await runtime.storage.markAttempted("mutation-1");
	const markUnknown = runtime.storage.markUnknown.bind(runtime.storage);
	runtime.storage.markUnknown = async (clientMutationId, state, options) => {
		const changed = await markUnknown(clientMutationId, state, options);
		runtime.registerTarget("hub-1", "ref-1", replacement);
		return changed;
	};
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);

	await expect(runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1", { authoritative: false }))).resolves.toBe("stale");
	expect(changes).toEqual([[targetKey], [targetKey]]);
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "blockedUnknown" });
	unsubscribe();
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

test("restart-required reads hold even never-attempted work until a later valid read", async () => {
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	client.on("turn/start", appliedReceipt);
	await registerAndStart(runtime, client);
	await runtime.submit(request("send"));

	// The thread's status carries the restart decision on its own, with no
	// resumeRequired flag riding along, so only the status branch can hold.
	const firstLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	expect(
		await runtime.reconcileAuthoritativeRead(
			firstLease!,
			readResponse("ref-1", { status: "restartRequired" }),
		),
	).toBe("blocked");
	expect(client.calls).toHaveLength(0);

	const secondLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	expect(await runtime.reconcileAuthoritativeRead(secondLease!, readResponse("ref-1"))).toBe("reconciled");
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

test("a read lease invalidated during the storage await is rejected after the await", async () => {
	let releaseResponse!: (error: unknown) => void;
	const pendingResponse = new Promise<never>((_resolve, reject) => {
		releaseResponse = reject;
	});
	let requestStarted!: () => void;
	const started = new Promise<void>((resolve) => {
		requestStarted = resolve;
	});
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => "mutation-1",
	});
	const client = new FakeClient("ready");
	let calls = 0;
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

	// A second read begins reconciling while its lease is still current; the
	// in-flight attempt then reports an unknown blocked outcome while that
	// reconcile is suspended inside its storage await, invalidating the lease
	// mid-flight.
	const midAwaitLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	const midAwait = runtime.reconcileAuthoritativeRead(midAwaitLease!, readResponse("ref-1"));
	releaseResponse(
		new WireError("journal unavailable", -32014, {
			evenerErrorInfo: "mutationOutcomeUnknown",
			clientMutationId: "mutation-1",
			mutationOutcome: "unknown",
			retryDisposition: "blocked",
			cause: "persistenceUnavailable",
		}),
	);
	expect(await midAwait).toBe("stale");
	expect(client.calls).toHaveLength(1);

	// The fence holds until a later authoritative read, and the retry keeps
	// the original mutation id.
	const laterLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(laterLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(2));
	expect(client.calls[1]?.params).toMatchObject({ clientMutationId: "mutation-1" });
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

test("a blocked outcome from a replaced client fences the replacement target", async () => {
	const database = openDatabase();
	const originalRunSync = database.runSync;
	let failMarkUnknown = false;
	database.runSync = (sql, ...params) => {
		if (failMarkUnknown && sql.includes("UPDATE mutation_outbox SET state = ?") && params[0] === "blockedUnknown")
			throw new Error("journal unavailable");
		return originalRunSync(sql, ...params);
	};
	const intervals: Array<() => void> = [];
	let releaseA!: (error: unknown) => void;
	const pendingA = new Promise<never>((_resolve, reject) => {
		releaseA = reject;
	});
	let callsA = 0;
	let callsB = 0;
	const runtime = new NativeMutationRuntime(database, {
		createMutationId: () => "mutation-1",
		setInterval: (callback) => {
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: () => undefined,
	});
	const clientA = new FakeClient("ready");
	clientA.on("turn/start", () => {
		callsA += 1;
		return pendingA as never;
	});
	const clientB = new FakeClient("ready");
	clientB.on("turn/start", (params) => {
		callsB += 1;
		return appliedReceipt(params);
	});
	runtime.registerTarget("hub-1", "ref-1", clientA);
	await runtime.start();
	await runtime.submit(request("send"));
	const initialLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", clientA);
	await runtime.reconcileAuthoritativeRead(initialLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(callsA).toBe(1));

	runtime.registerTarget("hub-1", "ref-1", clientB);
	const preOutcomeLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", clientB);
	await runtime.reconcileAuthoritativeRead(preOutcomeLease!, readResponse("ref-1"));
	expect(callsB).toBe(0);

	failMarkUnknown = true;
	releaseA(
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
	expect(callsB).toBe(0);

	const laterLease = runtime.beginAuthoritativeRead("hub-1", "ref-1", clientB);
	await runtime.reconcileAuthoritativeRead(laterLease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(callsB).toBe(1));
	expect(clientB.calls[0]?.params).toMatchObject({ clientMutationId: "mutation-1" });
	expect(callsA).toBe(1);
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
	const client = new FakeClient("connecting");
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
	const firstClient = new FakeClient("connecting");
	const replacementClient = new FakeClient("connecting");
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
	const firstClient = new FakeClient("connecting");
	const secondClient = new FakeClient("connecting");
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
	const firstClient = new FakeClient("connecting");
	const secondClient = new FakeClient("connecting");
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
	const client = new FakeClient("connecting");
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
	const client = new FakeClient("connecting");
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

test("stop cancels queued never-attempted work and the interrupt still dispatches", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("connecting");
	client.on("turn/interrupt", appliedReceipt);
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();

	await runtime.submit(request("send"));
	await runtime.submit(request("queue"));
	await runtime.submit(request("interrupt"));
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "canceled" });
	expect(await runtime.storage.getOutbox("mutation-2")).toMatchObject({ state: "canceled" });
	expect(client.calls).toHaveLength(0);

	client.emitStateChange("ready");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	expect(client.calls[0]?.params).toMatchObject({
		ref: "ref-1",
		clientMutationId: "mutation-3",
	});
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({ state: "canceled" });
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-3")).toBeUndefined();
	});
	await runtime.stop();
});

test("a stop landing between a submission's capture and its commit cancels the row", async () => {
	let nextId = 0;
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => `mutation-${++nextId}`,
	});
	const client = new FakeClient("connecting");
	client.on("turn/interrupt", appliedReceipt);
	runtime.registerTarget("hub-1", "ref-1", client);
	await runtime.start();

	// The send captures its click-time stop epoch first; the Stop's combined
	// write commits while the send is still between that capture and its
	// durable commit, so the send must commit born-canceled.
	const submission = runtime.submit(request("send"));
	await runtime.submit(request("interrupt"));
	await submission;
	expect(await runtime.storage.getOutbox("mutation-2")).toMatchObject({ state: "canceled" });
	expect(client.calls).toHaveLength(0);

	client.emitStateChange("ready");
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));
	await vi.waitFor(() => expect(client.calls).toHaveLength(1));
	expect(client.calls[0]?.params).toMatchObject({ clientMutationId: "mutation-1" });
	await runtime.stop();
});

test("the storage's cross-store id rejection never fences the runtime's same-id retry", async () => {
	// The id source is adversarial on purpose: #2043's guard exists for a
	// generator that repeats an id, and every enqueue below asks for
	// "mutation-1" until a healthy "mutation-2" proves the runtime survived
	// the rejections.
	const ids = ["mutation-1", "mutation-1", "mutation-1", "mutation-2"];
	let nextId = 0;
	const intervals: Array<() => void> = [];
	const runtime = new NativeMutationRuntime(openDatabase(), {
		createMutationId: () => ids[nextId++]!,
		setInterval: (callback) => {
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: () => undefined,
	});
	const client = new FakeClient("ready");
	const attempts: string[] = [];
	let firstAttempt!: () => void;
	const firstAttemptStarted = new Promise<void>((resolve) => {
		firstAttempt = resolve;
	});
	client.on("turn/start", (params) => {
		const { clientMutationId } = params as { clientMutationId: string };
		attempts.push(clientMutationId);
		if (attempts.length === 1) {
			firstAttempt();
			return Promise.reject(new Error("transport timeout")) as never;
		}
		return appliedReceipt(params);
	});
	await registerAndStart(runtime, client);
	const lease = runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await runtime.reconcileAuthoritativeRead(lease!, readResponse("ref-1"));

	await runtime.submit(request("send"));
	await firstAttemptStarted;
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({
			state: "submitting",
			attempted: true,
		});
	});

	// A fresh enqueue colliding with the still-active first record is
	// rejected by the storage's cross-store guard, and its sequence
	// allocation rolls back with it.
	await expect(runtime.submit(request("send"))).rejects.toThrow(/already active/);

	// The retry of the active record re-dispatches the stored row: no new
	// enqueue, so the guard never applies, and the id on the wire repeats.
	await vi.waitFor(() => {
		intervals[0]?.();
		expect(attempts).toEqual(["mutation-1", "mutation-1"]);
	});
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	});

	// The applied record now owns its id from the optimistic store, so the
	// same adversarial id is still rejected cross-store, not just table-local.
	await expect(runtime.submit(request("send"))).rejects.toThrow(/already active/);

	// The runtime is unharmed: a fresh id enqueues, dispatches, and the
	// rolled-back allocations left intentSequence gap-free.
	await runtime.submit(request("send"));
	await vi.waitFor(() => expect(attempts).toHaveLength(3));
	expect(attempts[2]).toBe("mutation-2");
	const settled = await runtime.storage.getOptimistic("mutation-2");
	expect(settled).toMatchObject({ state: "accepted", intentSequence: 2 });
	await runtime.stop();
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
	// The wire response stays held, so a submit that waited for the outcome
	// would never settle and this await would hang. Resolving here proves the
	// durable enqueue boundary alone released submit, while the record is
	// still in flight with no outcome processed.
	await submission;
	expect(await runtime.storage.getOutbox("mutation-1")).toMatchObject({
		state: "submitting",
		attempted: true,
	});

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
	await vi.waitFor(async () => {
		expect(await runtime.storage.getOutbox("mutation-1")).toBeUndefined();
	});
	await runtime.stop();
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
		instanceId: "instance-1",
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

test("a timer setup failure rejects the first start and the retry re-arms exactly one timer", async () => {
	const { runtime, intervals, cleared, attempts } = timerRecordingRuntime((attempt) => attempt === 1);
	const client = new FakeClient("connecting");
	runtime.registerTarget("hub-1", "ref-1", client);
	// A "connecting" client gates every scan but startup, so the only
	// discovery that runs is the outbox's startup scan. It reads the target
	// refs before onDiscover, so counting that read counts startup scans.
	let targetRefReads = 0;
	const originalListTargetRefs = runtime.storage.listTargetRefs.bind(runtime.storage);
	runtime.storage.listTargetRefs = async () => {
		targetRefReads += 1;
		return originalListTargetRefs();
	};

	await expect(runtime.start()).rejects.toThrow("timer setup unavailable");
	// The retry runs with no intervening stop: the failed start must not have
	// latched, or this second start would return early and never re-arm.
	await runtime.start();
	await vi.waitFor(() => expect(targetRefReads).toBe(1));

	expect(attempts()).toBe(2);
	expect(intervals).toHaveLength(1);
	expect(cleared).toEqual([]);
	// Exactly one startup scan: the failed setup scheduled none and the retry
	// scheduled one. Settle the queue before pinning the count.
	await new Promise((resolve) => setTimeout(resolve, 0));
	expect(targetRefReads).toBe(1);

	await runtime.stop();
	// stop stays safe and idempotent after the retry.
	await runtime.stop();
	expect(cleared).toEqual([1]);
});

test("stop during an in-flight start still cancels the acquired timer", async () => {
	const { runtime, intervals, cleared } = timerRecordingRuntime();
	const client = new FakeClient("connecting");
	runtime.registerTarget("hub-1", "ref-1", client);

	// The outbox acquires the timer synchronously and the start only settles a
	// microtask later. A stop that lands before that settle must still release
	// the acquired timer, and the settled start must not re-mark the runtime
	// started after shutdown finished.
	const pending = runtime.start();
	await runtime.stop();
	await pending;

	expect(intervals).toHaveLength(1);
	expect(cleared).toEqual([1]);
	// The runtime really is stopped: a later start re-arms exactly one timer.
	await runtime.start();
	expect(intervals).toHaveLength(2);
	await runtime.stop();
	expect(cleared).toEqual([1, 2]);
});

test("a stale failed start cannot clear a newer successful start", async () => {
	const { runtime, intervals, cleared, attempts } = timerRecordingRuntime((attempt) => attempt === 1);
	const client = new FakeClient("connecting");
	runtime.registerTarget("hub-1", "ref-1", client);

	// The first start rejects in a microtask. Before that settles, a stop and a
	// successful restart run and acquire the timer. The stale rejection's
	// rollback must not clear the newer start's started state.
	const failing = runtime.start();
	const stopping = runtime.stop();
	const restarting = runtime.start();
	await failing.catch(() => undefined);
	await stopping;
	await restarting;

	expect(attempts()).toBe(2);
	expect(intervals).toHaveLength(1);
	// The runtime is still started, so this stop releases the acquired timer.
	await runtime.stop();
	expect(cleared).toEqual([1]);
});

test("a concurrent start shares a failing attempt's rejection", async () => {
	const { runtime, intervals, cleared } = timerRecordingRuntime((attempt) => attempt === 1);
	const client = new FakeClient("connecting");
	runtime.registerTarget("hub-1", "ref-1", client);

	// Both calls run before the first attempt settles. They must share the one
	// attempt: the failure rejects both callers rather than telling one the
	// runtime started while the rollback leaves it stopped.
	const first = runtime.start();
	const second = runtime.start();
	await expect(first).rejects.toThrow("timer setup unavailable");
	await expect(second).rejects.toThrow("timer setup unavailable");

	// The runtime stays retryable: a fresh start re-arms exactly one timer.
	await runtime.start();
	expect(intervals).toHaveLength(1);
	await runtime.stop();
	expect(cleared).toEqual([1]);
});

test("a failed outbox setup unwinds its listeners and channel, and the retry re-arms once", async () => {
	const storage = new MutationOutboxSQLite(openDatabase(), {
		createMutationId: () => "mutation-1",
		now: () => 1,
	});
	let channelAdds = 0;
	let channelRemoves = 0;
	let channelCloses = 0;
	const channel: MutationOutboxChannel = {
		postMessage: () => undefined,
		close: () => {
			channelCloses += 1;
		},
		addEventListener: (_type: string, _listener: (event: unknown) => void) => {
			channelAdds += 1;
		},
		removeEventListener: (_type: string, _listener: (event: unknown) => void) => {
			channelRemoves += 1;
		},
	};
	let lifecycleAdds = 0;
	let lifecycleRemoves = 0;
	const lifecycle: MutationLifecycleTarget = {
		addEventListener: (_type: string, _listener: () => void) => {
			lifecycleAdds += 1;
		},
		removeEventListener: (_type: string, _listener: () => void) => {
			lifecycleRemoves += 1;
		},
	};
	const intervals: Array<() => void> = [];
	const cleared: number[] = [];
	let setupAttempts = 0;
	const discoveries: string[] = [];
	const outbox = new MutationOutbox(storage, {
		getClient: () => undefined,
		onDiscover: (_targetRefs, reason) => {
			discoveries.push(reason);
		},
		createBroadcastChannel: () => channel,
		lifecycleWindow: lifecycle,
		setInterval: (callback) => {
			setupAttempts += 1;
			if (setupAttempts === 1) throw new Error("timer setup unavailable");
			intervals.push(callback);
			return intervals.length;
		},
		clearInterval: (intervalId) => cleared.push(intervalId),
	});

	await expect(outbox.start()).rejects.toThrow("timer setup unavailable");
	// The rejected setup unwound the channel and both lifecycle listeners it
	// had already added, so nothing is left live behind it.
	expect(channelAdds).toBe(1);
	expect(channelRemoves).toBe(1);
	expect(channelCloses).toBe(1);
	expect(lifecycleAdds).toBe(2);
	expect(lifecycleRemoves).toBe(2);

	await outbox.start();
	await vi.waitFor(() => expect(discoveries).toEqual(["startup"]));
	expect(setupAttempts).toBe(2);
	expect(intervals).toHaveLength(1);
	expect(channelAdds).toBe(2);
	expect(channelRemoves).toBe(1);
	expect(channelCloses).toBe(1);
	expect(lifecycleAdds).toBe(4);
	expect(lifecycleRemoves).toBe(2);
	// Net live listeners are exactly the successful setup's - no duplicates.
	expect(channelAdds - channelRemoves).toBe(1);
	expect(lifecycleAdds - lifecycleRemoves).toBe(2);

	await outbox.stop();
	// stop stays safe and idempotent after the retry.
	await outbox.stop();
	expect(channelRemoves).toBe(2);
	expect(channelCloses).toBe(2);
	expect(lifecycleRemoves).toBe(4);
	expect(cleared).toEqual([1]);
});
