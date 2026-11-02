import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";
import { buildComposerInput, recoverMutationComposition, type InputAttachment } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
	NativeMutationRuntime,
	nativeMutationTargetKey,
	type NativeMutationRequest,
	type NativeMutationRuntimeOptions,
} from "./nativeMutationRuntime";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", async () => ({
	randomUUID: (await import("node:crypto")).randomUUID,
	getRandomValues: (array: Uint8Array) => array,
}));

const cleanups: Array<() => Promise<void>> = [];
afterEach(async () => {
	for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
const key = nativeMutationTargetKey("hub-1", "ref-1");
function request(extra: Partial<NativeMutationRequest> = {}): NativeMutationRequest {
	return {
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		kind: "send",
		input: [{ type: "text", text: "same" }],
		...extra,
	} as NativeMutationRequest;
}
function fixture(options: NativeMutationRuntimeOptions = {}) {
	const directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "queued-runtime-"));
	const path = join(directory, "mutations.sqlite");
	let opened = openSqliteSyncDouble(path);
	let next = 0;
	const runtimeOptions = { createMutationId: () => `mutation-${++next}`, ...options };
	let runtime = new NativeMutationRuntime(opened.port, runtimeOptions);
	cleanups.push(async () => {
		await runtime.stop();
		opened.database.close();
		rmSync(directory, { recursive: true });
	});
	return {
		get runtime() {
			return runtime;
		},
		get database() {
			return opened.database;
		},
		get port() {
			return opened.port;
		},
		async reopen() {
			await runtime.stop();
			opened.database.close();
			opened = openSqliteSyncDouble(path);
			runtime = new NativeMutationRuntime(opened.port, runtimeOptions);
			return runtime;
		},
	};
}

test("original composition metadata and encoded bytes survive runtime enqueue and file reopen", async () => {
	const f = fixture();
	const attachments: InputAttachment[] = [
		{ marker: 3, mediaType: "image/png", name: "first.png", data: "AP8H" },
		{ marker: 9, mediaType: "image/jpeg", name: "second.jpg", data: "AQID" },
	];
	const text = " /skill [image 9] then [image 3] /command ";
	const mentions = [
		{ kind: "skill" as const, name: "skill", offset: 1 },
		{ kind: "command" as const, name: "command", offset: 33 },
	];
	const input = buildComposerInput(text, attachments, ["skill"], ["command"], mentions);
	await f.runtime.submit(
		request({ input, composer: { text, attachments, mentions, skillNames: ["skill"], commandNames: ["command"] } }),
	);
	const runtime = await f.reopen();
	const [record] = (await runtime.read(key)).outbox;
	expect(record.composerText).toBe(text);
	expect(record.composerMentions).toEqual(mentions);
	expect(record.attachments).toEqual(
		attachments.map(({ data: _data, ...attachment }) => ({ ...attachment, presentationId: expect.any(String) })),
	);
	expect(new Set(record.attachments.map((attachment) => attachment.presentationId)).size).toBe(2);
	expect(recoverMutationComposition(record)).toEqual({
		text,
		attachments,
		mentions,
		skillNames: ["skill"],
		commandNames: ["command"],
		imageReferences: "recorded",
	});
	expect(record.payload).toEqual({
		ref: "ref-1",
		expectedInstanceId: "instance-1",
		input,
		clientMutationId: "mutation-1",
	});
	expect((record.payload.input as typeof input).filter((item) => item.type === "image")).toEqual([
		{ type: "image", mediaType: "image/png", name: "first.png", data: "AP8H" },
		{ type: "image", mediaType: "image/jpeg", name: "second.jpg", data: "AQID" },
	]);
});

test("legacy runtime requests do not invent original composer metadata", async () => {
	const f = fixture();
	await f.runtime.submit(
		request({
			input: [
				{ type: "image", mediaType: "image/png", data: "AP8H" },
				{ type: "text", text: "literal [image 9]" },
			],
		}),
	);
	const runtime = await f.reopen();
	const [record] = (await runtime.read(key)).outbox;
	expect(record.composerText).toBeUndefined();
	expect(record.composerMentions).toBeUndefined();
	expect(record.attachments).toEqual([]);
	expect(record.payload).not.toHaveProperty("composer");
});

async function source(runtime: NativeMutationRuntime, extra: Partial<NativeMutationRequest> = {}) {
	await runtime.submit(request(extra));
	const rows = (await runtime.read(key)).outbox;
	const record = rows[rows.length - 1];
	const recovery = await runtime.storage.transferToRecovery(record.clientMutationId, "editing");
	expect(recovery).toBeDefined();
	return record.clientMutationId;
}

function offline(runtime: NativeMutationRuntime, instanceId = "instance-1") {
	const client = new FakeClient("closed");
	runtime.registerTarget("hub-1", "ref-1", client, { threadId: "thread-1", instanceId });
	return client;
}

test("disconnected Edit and Remove claim one identity without starting or sending, and survive reopen", async () => {
	let starts = 0;
	const f = fixture({ setInterval: () => ++starts, clearInterval: () => {} });
	await f.runtime.submit(request());
	await f.runtime.submit(request());
	await f.runtime.submit(request());
	const runtime = await f.reopen();
	const client = offline(runtime);
	const notifications: string[][] = [];
	runtime.subscribeStorage((targets) => notifications.push([...targets]));
	const startsBefore = starts;
	const edited = await runtime.editQueuedMessage("mutation-1", key);
	expect(edited).toMatchObject({ clientMutationId: "mutation-1", recoveryKind: "editing" });
	expect(await runtime.removeQueuedMessage("mutation-2", key)).toBe(true);
	expect(await runtime.editQueuedMessage("mutation-1", key)).toBeUndefined();
	expect(await runtime.removeQueuedMessage("mutation-2", key)).toBe(false);
	expect(starts).toBe(startsBefore);
	expect(client.calls).toEqual([]);
	expect(notifications).toEqual([[key], [key], [key], [key]]);
	const reopened = await f.reopen();
	expect((await reopened.read(key)).outbox.map((row) => row.clientMutationId)).toEqual(["mutation-3"]);
	expect((await reopened.read(key)).recovery.map((row) => row.clientMutationId)).toEqual(["mutation-1"]);
});

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s rejects stale target/session and refreshes losing claims",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		const runtime = await f.reopen();
		offline(runtime, "replacement-instance");
		const notifications: string[][] = [];
		runtime.subscribeStorage((targets) => notifications.push([...targets]));
		expect(await runtime[action]("mutation-1", key)).toBe(action === "editQueuedMessage" ? undefined : false);
		expect(await runtime[action]("mutation-1", nativeMutationTargetKey("other-hub", "ref-1"))).toBe(
			action === "editQueuedMessage" ? undefined : false,
		);
		expect((await runtime.read(key)).outbox).toHaveLength(1);
		expect((await runtime.read(key)).recovery).toEqual([]);
		expect(notifications).toHaveLength(2);
	},
);

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s loses to newer Stop, admission and registration replacement",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		offline(f.runtime);
		const stopRace = f.runtime[action]("mutation-1", key);
		await f.runtime.storage.enqueueInterruptAndCancel({
			targetRef: key,
			method: "turn/interrupt",
			payload: { ref: "ref-1" },
			attachments: [],
			optimisticDisplay: null,
		});
		expect(await stopRace).toBe(action === "editQueuedMessage" ? undefined : false);
		await f.runtime.submit(request());
		const admissionRace = f.runtime[action]("mutation-3", key);
		await f.runtime.storage.markAttempted("mutation-3");
		expect(await admissionRace).toBe(action === "editQueuedMessage" ? undefined : false);
		const replacementRace = f.runtime[action]("mutation-1", key);
		offline(f.runtime, "new-instance");
		expect(await replacementRace).toBe(action === "editQueuedMessage" ? undefined : false);
		expect((await f.runtime.read(key)).recovery).toEqual([]);
		expect((await f.runtime.read(key)).outbox).toHaveLength(3);
	},
);

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s retains its source after a real SQLite write failure",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		offline(f.runtime);
		f.database.exec("PRAGMA query_only = ON");
		await expect(f.runtime[action]("mutation-1", key)).rejects.toBeInstanceOf(Error);
		f.database.exec("PRAGMA query_only = OFF");
		expect((await f.runtime.read(key)).outbox).toHaveLength(1);
		expect((await f.runtime.read(key)).recovery).toEqual([]);
		expect(await f.runtime[action]("mutation-1", key)).toBeTruthy();
	},
);

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s rechecks session ownership at the claim, not in an earlier helper continuation",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		offline(f.runtime);
		const claiming = f.runtime[action]("mutation-1", key);
		await Promise.resolve();
		offline(f.runtime, "replacement-instance");
		expect(await claiming).toBe(action === "editQueuedMessage" ? undefined : false);
		expect((await f.runtime.read(key)).outbox).toHaveLength(1);
	},
);

test("all-source resend atomically consumes exactly its sources and retains an exact receipt after settlement and reopen", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const second = await source(f.runtime);
	const other = await source(f.runtime);
	const notifications: string[][] = [];
	f.runtime.subscribeStorage((targets) => notifications.push([...targets]));
	await f.runtime.submit(request({ recoverySourceIds: [first, second] }));
	const snapshot = await f.runtime.read(key);
	expect(snapshot.recovery.map((row) => row.clientMutationId)).toEqual([other]);
	expect(snapshot.outbox.map((row) => row.clientMutationId)).toEqual(["mutation-4"]);
	expect(f.runtime.submittedHere(key).has("mutation-4")).toBe(true);
	expect(notifications).toEqual([[key]]);
	expect(await f.runtime.readRecoveryResend(key, [first])).toBeUndefined();
	expect(await f.runtime.readRecoveryResend(key, [first, second, other])).toBeUndefined();
	const receipt = { targetRef: key, sourceIds: [first, second], clientMutationId: "mutation-4" };
	expect(await f.runtime.readRecoveryResend(key, [second, first])).toEqual(receipt);
	await f.runtime.settleIdentities(["mutation-4"]);
	const runtime = await f.reopen();
	expect((await runtime.read(key)).outbox).toEqual([]);
	expect(await runtime.readRecoveryResend(key, [first, second])).toEqual(receipt);
	await runtime.acknowledgeRecoveryResend(nativeMutationTargetKey("other-hub", "ref-1"), "mutation-4");
	expect(await runtime.readRecoveryResend(key, [first, second])).toEqual(receipt);
	await runtime.acknowledgeRecoveryResend(key, "mutation-4");
	expect(await (await f.reopen()).readRecoveryResend(key, [first, second])).toBeUndefined();
});

test("missing and repeated recovery sources never enqueue or send a fallback", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const client = new FakeClient("ready");
	client.on("turn/start", () => {
		throw new Error("unexpected send");
	});
	f.runtime.registerTarget("hub-1", "ref-1", client);
	await expect(f.runtime.submit(request({ recoverySourceIds: [first, "missing"] }))).rejects.toBeInstanceOf(Error);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect((await f.runtime.read(key)).recovery.map((row) => row.clientMutationId)).toEqual([first]);
	await f.runtime.submit(request({ recoverySourceIds: [first] }));
	await expect(f.runtime.submit(request({ recoverySourceIds: [first] }))).rejects.toBeInstanceOf(Error);
	expect((await f.runtime.read(key)).outbox).toHaveLength(1);
	expect(client.calls).toEqual([]);
});

test("storage failure in a recovery resend never falls back, while ordinary submit still does", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const client = new FakeClient("ready");
	client.on("turn/start", () => ({}) as never);
	f.runtime.registerTarget("hub-1", "ref-1", client);
	f.database.exec("PRAGMA query_only = ON");
	await expect(f.runtime.submit(request({ recoverySourceIds: [first] }))).rejects.toBeInstanceOf(Error);
	expect(client.calls).toEqual([]);
	await f.runtime.submit(request());
	expect(client.calls.map((call) => call.method)).toEqual(["turn/start"]);
	f.database.exec("PRAGMA query_only = OFF");
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect((await f.runtime.read(key)).recovery.map((row) => row.clientMutationId)).toEqual([first]);
});

test("a receipt-write failure rolls back every source deletion and the fresh resend", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const second = await source(f.runtime);
	f.database.exec(
		"CREATE TRIGGER fail_receipt BEFORE UPDATE OF recovery_resends ON mutation_sequence BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END",
	);
	await expect(f.runtime.submit(request({ recoverySourceIds: [first, second] }))).rejects.toBeInstanceOf(Error);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect((await f.runtime.read(key)).recovery.map((row) => row.clientMutationId)).toEqual([first, second]);
	f.database.exec("DROP TRIGGER fail_receipt");
	await f.runtime.submit(request({ recoverySourceIds: [first, second] }));
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect((await f.runtime.read(key)).outbox).toHaveLength(1);
});

test("a newer Stop during asynchronous startup holds the fresh resend and its receipt", async () => {
	let duringStartup: (() => void) | undefined;
	const f = fixture({
		setInterval: () => {
			duringStartup?.();
			return 1;
		},
		clearInterval: () => {},
	});
	const first = await source(f.runtime);
	await f.runtime.stop();
	// A running runtime delivers Stop through the app's one SQLite connection
	// while the restarted submitter awaits startup. Only timer setup is scripted.
	const stopper = new NativeMutationRuntime(f.port, { createMutationId: () => "stop-1" });
	cleanups.push(() => stopper.stop());
	await stopper.start();
	let stopped: Promise<unknown> | undefined;
	duringStartup = () => {
		stopped = stopper.submit(request({ kind: "interrupt", input: [] }));
	};
	await f.runtime.submit(request({ recoverySourceIds: [first] }));
	await stopped;
	const snapshot = await f.runtime.read(key);
	expect(snapshot.recovery).toEqual([]);
	expect(snapshot.outbox.find((row) => row.method === "turn/start")).toMatchObject({
		clientMutationId: "mutation-2",
		state: "canceled",
		attempted: false,
	});
	expect(await f.runtime.readRecoveryResend(key, [first])).toMatchObject({ clientMutationId: "mutation-2" });
});

test("cached identity enables local claims with no client after recreation and leaves transport leases alone", async () => {
	const f = fixture();
	await f.runtime.submit(request());
	const runtime = await f.reopen();
	const release = runtime.registerTarget("hub-1", "ref-1", null, { threadId: "thread-1", instanceId: "instance-1" });
	expect(runtime.targetClient("hub-1", "ref-1")).toBeUndefined();
	expect(await runtime.editQueuedMessage("mutation-1", key)).toMatchObject({ recoveryKind: "editing" });
	release();
	const client = offline(runtime);
	const releaseLocal = runtime.registerTarget("hub-1", "ref-1", null, {
		threadId: "thread-1",
		instanceId: "instance-1",
	});
	releaseLocal();
	expect(runtime.targetClient("hub-1", "ref-1")).toBe(client);
});

test("an old transport registration cleanup cannot delete a newer offline identity owner", async () => {
	const f = fixture();
	await f.runtime.submit(request());
	const old = f.runtime.registerTarget("hub-1", "ref-1", new FakeClient("closed"));
	f.runtime.registerTarget("hub-1", "ref-1", null, { threadId: "thread-1", instanceId: "instance-1" });
	old();
	expect(await f.runtime.removeQueuedMessage("mutation-1", key)).toBe(true);
});

test("a fresh resend enters normal dispatch only after all sources and receipt commit", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const second = await source(f.runtime);
	const client = new FakeClient("ready");
	f.runtime.registerTarget("hub-1", "ref-1", client);
	const lease = f.runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
	await f.runtime.reconcileAuthoritativeRead(lease!, {
		thread: {
			id: "thread-1",
			status: { type: "idle" },
			evener: {
				queue: { revision: 0, clientMutationIds: [] },
				ref: "ref-1",
				instanceId: "instance-1",
				mutationStateAuthoritative: true,
			},
		},
	} as unknown as import("@evener/appwire-client").ThreadReadResponse);
	let delivered: unknown;
	client.on("turn/start", async (params) => {
		delivered = params;
		expect((await f.runtime.read(key)).recovery).toEqual([]);
		expect(await f.runtime.readRecoveryResend(key, [first, second])).toMatchObject({
			clientMutationId: params.clientMutationId,
		});
		return {
			receipt: {
				clientMutationId: params.clientMutationId,
				disposition: "applied",
				threadId: "thread-1",
				turnId: "turn-1",
				projectionState: "pending",
			},
			turn: { id: "turn-1" },
		} as never;
	});
	await f.runtime.submit(request({ recoverySourceIds: [first, second] }));
	await vi.waitFor(async () => expect(await f.runtime.storage.getOptimistic("mutation-3")).toBeDefined());
	expect(delivered).toEqual({
		ref: "ref-1",
		expectedInstanceId: "instance-1",
		input: [{ type: "text", text: "same" }],
		clientMutationId: "mutation-3",
	});
	expect(client.calls).toHaveLength(1);
	expect(f.runtime.submittedHere(key).has("mutation-3")).toBe(true);
});

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s honors a newer authoritative session and never treats attempted delivery as unsent",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		const client = new FakeClient("ready");
		f.runtime.registerTarget("hub-1", "ref-1", client);
		const lease = f.runtime.beginAuthoritativeRead("hub-1", "ref-1", client);
		await f.runtime.reconcileAuthoritativeRead(lease!, {
			thread: {
				id: "thread-1",
				status: { type: "restartRequired" },
				evener: { queue: { revision: 0, clientMutationIds: [] }, ref: "ref-1", instanceId: "replacement-instance" },
			},
		} as unknown as import("@evener/appwire-client").ThreadReadResponse);
		client.emitStateChange("closed");
		expect(await f.runtime[action]("mutation-1", key)).toBe(action === "editQueuedMessage" ? undefined : false);
		offline(f.runtime);
		await f.runtime.storage.markAttempted("mutation-1");
		expect(await f.runtime[action]("mutation-1", key)).toBe(action === "editQueuedMessage" ? undefined : false);
		await f.runtime.storage.markUnknown("mutation-1", "blockedUnknown");
		expect(await f.runtime[action]("mutation-1", key)).toBe(action === "editQueuedMessage" ? undefined : false);
		await f.runtime.settleIdentities(["mutation-1"]);
		expect(await f.runtime[action]("mutation-1", key)).toBe(action === "editQueuedMessage" ? undefined : false);
		expect(client.calls).toEqual([]);
	},
);

test("concurrent all-source resends create exactly one fresh owner", async () => {
	const f = fixture();
	const first = await source(f.runtime);
	const second = await source(f.runtime);
	const results = await Promise.allSettled([
		f.runtime.submit(request({ recoverySourceIds: [first, second] })),
		f.runtime.submit(request({ recoverySourceIds: [second, first] })),
	]);
	expect(results.map((result) => result.status).sort()).toEqual(["fulfilled", "rejected"]);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect((await f.runtime.read(key)).outbox.map((row) => row.clientMutationId)).toEqual(["mutation-3"]);
	expect(await f.runtime.readRecoveryResend(key, [first, second])).toEqual({
		targetRef: key,
		sourceIds: [first, second],
		clientMutationId: "mutation-3",
	});
});

test.each(["editQueuedMessage", "removeQueuedMessage"] as const)(
	"%s loses to explicit Retry without changing its identity or order",
	async (action) => {
		const f = fixture();
		await f.runtime.submit(request());
		await f.runtime.submit(request({ kind: "interrupt", input: [] }));
		offline(f.runtime);
		const retry = f.runtime.releaseCanceled("mutation-1", key);
		const claim = f.runtime[action]("mutation-1", key);
		expect(await retry).toBe(true);
		expect(await claim).toBe(action === "editQueuedMessage" ? undefined : false);
		const snapshot = await f.runtime.read(key);
		expect(snapshot.recovery).toEqual([]);
		expect(snapshot.outbox.map((row) => [row.clientMutationId, row.intentSequence, row.state])).toEqual([
			["mutation-2", 2, "submitting"],
			["mutation-1", 3, "submitting"],
		]);
	},
);
