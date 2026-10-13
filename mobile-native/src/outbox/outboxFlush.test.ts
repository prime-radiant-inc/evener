import type { ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, expect, it, vi } from "vitest";
import { reconnectDelay } from "../hubConnection";
import { NativeMutationRuntime, type NativeMutationRequest } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { OutboxFlush, parseTargetKey } from "./outboxFlush";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "test-uuid", getRandomValues: (array: Uint8Array) => array }));

let database: SqliteDoubleDatabase | undefined;
afterEach(() => {
	database?.close();
	database = undefined;
});

function runtime(): NativeMutationRuntime {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	let next = 0;
	return new NativeMutationRuntime(opened.port, { createMutationId: () => `mutation-${++next}` });
}

const message = (over: Partial<Exclude<NativeMutationRequest, { kind: "promote" }>> = {}): NativeMutationRequest => ({
	kind: "send",
	hubId: "hub-1",
	targetRef: "ref-1",
	threadId: "thread-1",
	instanceId: "instance-1",
	input: [{ type: "text", text: "sent on the train" }],
	...over,
});

function applied(params: unknown): never {
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

function read(ref: string): ThreadReadResponse {
	return {
		thread: {
			id: "thread-1",
			status: { type: "idle" },
			evener: { ref, capabilities: {}, queue: { clientMutationIds: [] }, mutationStateAuthoritative: true },
		},
	} as unknown as ThreadReadResponse;
}

function methods(client: FakeClient): string[] {
	return client.calls.map((call) => call.method);
}

/** Every settle the flush starts, in order. The flush awaits each one before
 * a test can, so once a test's await of one resolves, that settle's end in
 * the flush has run. */
function settles(outbox: NativeMutationRuntime): Promise<unknown>[] {
	const started: Promise<unknown>[] = [];
	const settleTarget = outbox.settleTarget.bind(outbox);
	vi.spyOn(outbox, "settleTarget").mockImplementation((...args) => {
		const settle = settleTarget(...args);
		started.push(settle);
		return settle;
	});
	return started;
}

it("sends a message kept while offline once the connection returns, even after a relaunch", async () => {
	const opened = openSqliteSyncDouble();
	database = opened.database;
	const beforeRelaunch = new NativeMutationRuntime(opened.port, { createMutationId: () => "mutation-1" });
	await beforeRelaunch.submit(message());
	await beforeRelaunch.stop();
	// A fresh process: the runtime is new and not started; the message is on disk.
	const outbox = new NativeMutationRuntime(opened.port, { createMutationId: () => "mutation-2" });
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	expect(client.calls[0]?.params).toEqual({ ref: "ref-1", includeTurns: true, itemsView: "fragment", itemLimit: 40 });
	expect((client.calls[1]?.params as { clientMutationId: string }).clientMutationId).toBe("mutation-1");
	await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
	flush.dispose();
	await outbox.stop();
});

it("never takes a session a screen holds", async () => {
	const outbox = runtime();
	const screen = new FakeClient("ready");
	outbox.registerTarget("hub-1", "ref-1", screen);
	await outbox.submit(message());
	const client = new FakeClient("ready");
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	expect(methods(client)).toEqual([]);
	flush.dispose();
	await outbox.stop();
});

it("settles for a session screen that isn't reading, and leaves the target the screen's", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	// A session screen under the Reader: registered with the connection's
	// client, blocked since the reconnect, and not reading while blurred.
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	await outbox.submit(message());
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	flush.dispose();
	expect(outbox.targetClient("hub-1", "ref-1")).toBe(client);
	unregister();
	await outbox.stop();
});

it("settles a covered screen's target once, however many flushes run beside each other", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	const pending: { answer?: (response: ThreadReadResponse) => void } = {};
	let reads = 0;
	client.on("thread/read", () => {
		reads += 1;
		return new Promise<ThreadReadResponse>((resolve) => {
			pending.answer = resolve;
		});
	});
	client.on("turn/start", applied);
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	await outbox.submit(message());
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(reads).toBe(1));

	await flush.flush();
	expect(reads).toBe(1);

	pending.answer?.(read("ref-1"));
	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	flush.dispose();
	unregister();
	await outbox.stop();
});

it("keeps a new connection's mark on a screen's target when an older settle finishes late", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const reads = (client: FakeClient) => {
		const held: { answer?: (response: ThreadReadResponse) => void; count: number } = { count: 0 };
		client.on("thread/read", () => {
			held.count += 1;
			return new Promise<ThreadReadResponse>((resolve) => {
				held.answer = resolve;
			});
		});
		client.on("turn/start", applied);
		return held;
	};
	const old = new FakeClient("ready");
	const oldReads = reads(old);
	const fresh = new FakeClient("ready");
	const freshReads = reads(fresh);
	const started = settles(outbox);
	// A covered session screen, registered with the connection it sees.
	outbox.registerTarget("hub-1", "ref-1", old);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", old);
	await vi.waitFor(() => expect(oldReads.count).toBe(1));
	// The connection is replaced, and the screen registers with the new one.
	const unregister = outbox.registerTarget("hub-1", "ref-1", fresh);
	flush.bind("hub-1", fresh);
	await vi.waitFor(() => expect(freshReads.count).toBe(1));

	// The old read answers late; its settle ends without clearing the new mark.
	oldReads.answer?.(read("ref-1"));
	await expect(started[0]).resolves.toBe("stale");
	await flush.flush();
	expect(freshReads.count).toBe(1);

	freshReads.answer?.(read("ref-1"));
	await vi.waitFor(() => expect(methods(fresh)).toEqual(["thread/read", "turn/start"]));
	expect(methods(old)).toEqual(["thread/read"]);
	flush.dispose();
	unregister();
	await outbox.stop();
});

it("reads nothing for a session screen with nothing waiting to send", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	await outbox.submit(message());
	// Its one message couldn't be confirmed, so it waits for you in its session.
	await outbox.storage.markUnknown("mutation-1", "blockedUnknown");
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	expect(methods(client)).toEqual([]);
	flush.dispose();
	unregister();
	await outbox.stop();
});

it("sends what another screen admits for a session no screen has open", async () => {
	const outbox = runtime();
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await flush.flush();
	expect(methods(client)).toEqual([]);

	await outbox.submit(message());

	await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
	flush.dispose();
	await outbox.stop();
});

it("keeps a message it can't settle, and lets its session go", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => Promise.reject(new Error("offline")));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);
	await flush.flush();

	await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
	expect(methods(client)).not.toContain("turn/start");
	expect((await outbox.storage.getOutbox("mutation-1"))?.state).toBe("submitting");
	flush.dispose();
	await outbox.stop();
});

it("lets go of a target it can't settle, and still sends the others", async () => {
	const outbox = runtime();
	await outbox.submit(message({ targetRef: "ref-1" }));
	await outbox.submit(message({ targetRef: "ref-2" }));
	const client = new FakeClient("ready");
	// ref-1's answer is malformed, so settling it throws.
	client.on("thread/read", (params) => ((params as { ref: string }).ref === "ref-1" ? ({} as never) : read("ref-2")));
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toContain("turn/start"));
	expect((client.calls.find((call) => call.method === "turn/start")?.params as { ref: string }).ref).toBe("ref-2");
	expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined();
	flush.dispose();
	await outbox.stop();
});

it("settles every target at once, so a read the hub is slow to answer holds up no other", async () => {
	const outbox = runtime();
	await outbox.submit(message({ targetRef: "ref-1" }));
	await outbox.submit(message({ targetRef: "ref-2" }));
	const client = new FakeClient("ready");
	let reads = 0;
	// The first read, whichever target it is for, never answers.
	client.on("thread/read", (params) =>
		++reads === 1 ? new Promise<never>(() => {}) : read((params as { ref: string }).ref),
	);
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);

	flush.bind("hub-1", client);

	await vi.waitFor(() => expect(methods(client)).toContain("turn/start"));
	expect(reads).toBe(2);
	flush.dispose();
	await outbox.stop();
});

it("tries again after a backoff when storage fails before it can list what waits", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const outbox = runtime();
		await outbox.submit(message());
		vi.spyOn(outbox, "start").mockRejectedValueOnce(new Error("the database is busy"));
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("ref-1"));
		client.on("turn/start", applied);
		const flush = new OutboxFlush(() => outbox);

		flush.bind("hub-1", client);
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) - 1);
		expect(methods(client)).toEqual([]);
		await vi.advanceTimersByTimeAsync(1);

		await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
		flush.dispose();
		await outbox.stop();
	} finally {
		vi.useRealTimers();
	}
});

it("looks again for a record that landed while a settle it then lost was in flight", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	const firstRead: { reject?: (error: Error) => void } = {};
	let reads = 0;
	client.on("thread/read", () => {
		reads += 1;
		if (reads > 1) return read("ref-1");
		return new Promise<never>((_resolve, reject) => {
			firstRead.reject = reject;
		});
	});
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(firstRead.reject).toBeDefined());

	await outbox.submit(message({ input: [{ type: "text", text: "and this one" }] }));
	firstRead.reject?.(new Error("the connection dropped the answer"));

	await vi.waitFor(() => expect(methods(client).filter((method) => method === "turn/start")).toHaveLength(2));
	flush.dispose();
	await outbox.stop();
});

it("lets only a settle's own end release its claim, so a record that lands meanwhile still goes", async () => {
	const outbox = runtime();
	const key = JSON.stringify(["hub-1", "ref-1"]);
	const client = new FakeClient("ready");
	const reads: ((response: ThreadReadResponse) => void)[] = [];
	client.on(
		"thread/read",
		() =>
			new Promise<ThreadReadResponse>((resolve) => {
				reads.push(resolve);
			}),
	);
	client.on("turn/start", applied);
	const answerReads = () => {
		for (const answer of reads.splice(0)) answer(read("ref-1"));
	};
	const starts = () => methods(client).filter((method) => method === "turn/start").length;
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	// A first message goes, and leaves the target listed by its accepted row.
	await outbox.submit(message());
	await vi.waitFor(() => expect(reads).toHaveLength(1));
	answerReads();
	await vi.waitFor(() => expect(starts()).toBe(1));
	await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());

	// Another flush claims the target and waits on its read.
	void flush.flush();
	await vi.waitFor(() => expect(reads).toHaveLength(1));
	// A storage change with nothing waiting, then a second message, both mid-settle.
	await outbox.discardRecovery("nothing-here", key);
	await outbox.submit(message({ input: [{ type: "text", text: "the second" }] }));
	answerReads();
	await vi.waitFor(() => answerReads() === undefined && expect(starts()).toBe(2));
	// One read for each settle: the change mid-settle claimed nothing new.
	expect(methods(client).filter((method) => method === "thread/read")).toHaveLength(2);
	flush.dispose();
	await outbox.stop();
});

it("leaves sending to the runtime: a record for a target it holds open goes without another settle", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	const starts: (() => void)[] = [];
	client.on(
		"turn/start",
		(params) =>
			new Promise((resolve) => {
				starts.push(() => resolve(applied(params)));
			}),
	);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	// The first send is out, so the flush keeps its claim: something still waits.
	await vi.waitFor(() => expect(starts).toHaveLength(1));
	expect(outbox.targetClient("hub-1", "ref-1")).toBe(client);

	await outbox.submit(message({ input: [{ type: "text", text: "the second" }] }));
	starts.shift()?.();
	await vi.waitFor(() => expect(starts).toHaveLength(1));
	starts.shift()?.();

	await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
	expect(methods(client)).toEqual(["thread/read", "turn/start", "turn/start"]);
	flush.dispose();
	await outbox.stop();
});

it("never uses a read an older connection started, nor lets it touch the new one's", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const held = (client: FakeClient) => {
		const read: { answer?: (response: ThreadReadResponse) => void } = {};
		client.on(
			"thread/read",
			() =>
				new Promise<ThreadReadResponse>((resolve) => {
					read.answer = resolve;
				}),
		);
		client.on("turn/start", applied);
		return read;
	};
	const old = new FakeClient("ready");
	const oldRead = held(old);
	const fresh = new FakeClient("ready");
	const freshRead = held(fresh);
	const started = settles(outbox);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", old);
	await vi.waitFor(() => expect(oldRead.answer).toBeDefined());
	flush.bind("hub-1", fresh);
	await vi.waitFor(() => expect(freshRead.answer).toBeDefined());

	// The old read answers late, while the new connection's own read is out.
	// The runtime calls it stale, and the old settle leaves the new claim be.
	oldRead.answer?.(read("ref-1"));
	await expect(started[0]).resolves.toBe("stale");
	freshRead.answer?.(read("ref-1"));

	await vi.waitFor(() => expect(methods(fresh)).toEqual(["thread/read", "turn/start"]));
	expect(methods(old)).toEqual(["thread/read"]);
	flush.dispose();
	await outbox.stop();
});

it("sends only the active hub's messages, and nothing while the connection isn't ready", async () => {
	const outbox = runtime();
	await outbox.submit(message({ hubId: "hub-2" }));
	const client = new FakeClient("ready");
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await flush.flush();
	expect(methods(client)).toEqual([]);

	const connecting = new FakeClient("connecting");
	flush.bind("hub-2", connecting);
	await flush.flush();
	expect(methods(connecting)).toEqual([]);
	flush.dispose();
	await outbox.stop();
});

it("lets every session go when the connection drops", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => new Promise<never>(() => {}));
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBe(client));

	flush.bind("hub-1", null);

	expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined();
	flush.dispose();
	await outbox.stop();
});

it("lets go of a target whose outbox can't be read after it settles, and looks again after a backoff (#2708)", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const outbox = runtime();
		await outbox.submit(message());
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("ref-1"));
		client.on("turn/start", applied);
		// The read after the settle, which decides whether anything still
		// waits, fails once.
		const listOutbox = outbox.storage.listOutbox.bind(outbox.storage);
		let failed = false;
		vi.spyOn(outbox.storage, "listOutbox").mockImplementation(async (...args) => {
			if (!failed && methods(client).includes("turn/start")) {
				failed = true;
				throw new Error("the database is busy");
			}
			return listOutbox(...args);
		});
		const flush = new OutboxFlush(() => outbox);

		flush.bind("hub-1", client);
		await vi.waitFor(() => expect(failed).toBe(true));
		// The claim goes, rather than holding the target until the next
		// reconnect or storage change.
		await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
		const reads = () => methods(client).filter((method) => method === "thread/read").length;
		const before = reads();
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));
		await vi.waitFor(() => expect(reads()).toBe(before + 1));
		await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
		flush.dispose();
		await outbox.stop();
	} finally {
		vi.useRealTimers();
	}
});

it("takes a target back once a screen that took it over lets it go", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	client.on("thread/read", () => read("ref-1"));
	// The first send is out, so the flush keeps its claim.
	const starts: (() => void)[] = [];
	client.on(
		"turn/start",
		(params) =>
			new Promise((resolve) => {
				starts.push(() => resolve(applied(params)));
			}),
	);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(starts).toHaveLength(1));
	// A session screen opens the session, taking the target over, and leaves.
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	unregister();
	expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined();

	await flush.flush();

	await vi.waitFor(() => expect(methods(client).filter((method) => method === "thread/read")).toHaveLength(2));
	flush.dispose();
	await outbox.stop();
});

it("backs off further while the outbox stays unreadable after each settle", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const outbox = runtime();
		await outbox.submit(message());
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("ref-1"));
		client.on("turn/start", applied);
		const listOutbox = outbox.storage.listOutbox.bind(outbox.storage);
		vi.spyOn(outbox.storage, "listOutbox").mockImplementation(async (...args) => {
			if (methods(client).includes("turn/start")) throw new Error("the database is busy");
			return listOutbox(...args);
		});
		const reads = () => methods(client).filter((method) => method === "thread/read").length;
		const flush = new OutboxFlush(() => outbox);
		flush.bind("hub-1", client);
		await vi.waitFor(() => expect(methods(client)).toContain("turn/start"));
		await vi.waitFor(() => expect(outbox.targetClient("hub-1", "ref-1")).toBeUndefined());
		const first = reads();
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));
		await vi.waitFor(() => expect(reads()).toBe(first + 1));
		// The second failure in a row waits the second step, not the first again.
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));
		expect(reads()).toBe(first + 1);
		await vi.advanceTimersByTimeAsync(reconnectDelay(2) - reconnectDelay(1));
		await vi.waitFor(() => expect(reads()).toBe(first + 2));
		flush.dispose();
		await outbox.stop();
	} finally {
		vi.useRealTimers();
	}
});

it("never throws when the mutations database can't open, and looks again after a backoff", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const outbox = runtime();
		await outbox.submit(message());
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("ref-1"));
		client.on("turn/start", applied);
		let opens = 0;
		const flush = new OutboxFlush(() => {
			opens += 1;
			if (opens === 1) throw new Error("the mutations database couldn't open");
			return outbox;
		});

		expect(() => flush.bind("hub-1", client)).not.toThrow();
		await expect(flush.flush()).resolves.toBeUndefined();
		await vi.advanceTimersByTimeAsync(reconnectDelay(1));

		await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
		flush.dispose();
		await outbox.stop();
	} finally {
		vi.useRealTimers();
	}
});

it("looks again after a backoff when storage fails while settling for a covered screen", async () => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
	try {
		const outbox = runtime();
		const client = new FakeClient("ready");
		client.on("thread/read", () => read("ref-1"));
		client.on("turn/start", applied);
		// A session screen under the Reader, holding its target.
		const unregister = outbox.registerTarget("hub-1", "ref-1", client);
		await outbox.submit(message());
		vi.spyOn(outbox.storage, "listOutbox").mockRejectedValueOnce(new Error("the database is busy"));
		const flush = new OutboxFlush(() => outbox);

		flush.bind("hub-1", client);
		await vi.advanceTimersByTimeAsync(reconnectDelay(1) - 1);
		expect(methods(client)).toEqual([]);
		await vi.advanceTimersByTimeAsync(1);

		await vi.waitFor(() => expect(methods(client)).toEqual(["thread/read", "turn/start"]));
		flush.dispose();
		unregister();
		await outbox.stop();
	} finally {
		vi.useRealTimers();
	}
});

it("takes a target back when a screen took it over and let it go while the flush's read was out", async () => {
	const outbox = runtime();
	await outbox.submit(message());
	const client = new FakeClient("ready");
	const reads: ((response: ThreadReadResponse) => void)[] = [];
	client.on(
		"thread/read",
		() =>
			new Promise<ThreadReadResponse>((resolve) => {
				reads.push(resolve);
			}),
	);
	client.on("turn/start", applied);
	const flush = new OutboxFlush(() => outbox);
	flush.bind("hub-1", client);
	await vi.waitFor(() => expect(reads).toHaveLength(1));
	// A session screen opens and leaves while that read is out, and asks the
	// flush to look as it goes.
	const unregister = outbox.registerTarget("hub-1", "ref-1", client);
	unregister();
	await flush.flush();

	// The old read answers stale; the flush looks again and sends.
	reads.shift()?.(read("ref-1"));
	await vi.waitFor(() => expect(reads).toHaveLength(1));
	reads.shift()?.(read("ref-1"));
	await vi.waitFor(() => expect(methods(client)).toContain("turn/start"));
	flush.dispose();
	await outbox.stop();
});

it("reads the hub and ref out of a composite target key", () => {
	expect(parseTargetKey(JSON.stringify(["hub-1", "local:thread-1"]))).toEqual({
		hubId: "hub-1",
		ref: "local:thread-1",
	});
	expect(parseTargetKey("local:thread-1")).toBeNull();
	expect(parseTargetKey(JSON.stringify(["hub-1"]))).toBeNull();
});
