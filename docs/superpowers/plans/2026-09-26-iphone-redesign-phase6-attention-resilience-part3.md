# iPhone redesign, Phase 6: Attention and resilience (Implementation Plan), part 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

This is part 3 of `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience.md` (part 1: PRs A, E and F, Tasks 1-2 and 12-14; part 2: PRs B, C, D, G and H). Part 1's Goal, Architecture, Tech Stack, Spec, Global Constraints, Rulings (ruling 17 is this part's), "Built on earlier phases" table and Review Focus (item 4) bind this task, and its task number is part 1's. PR I starts once part 1's PR F has landed.

## What part 1's review left here

Part 1's review (#2511) raised Task 15's flush in rounds 3, 4, 6, 7, 8 and 9; each round's fixes are in the code below. Round 9 left one Medium, which moved here with the task and is fixed below: a covered screen's target could be settled twice at once. The flush now marks a screen's target while it settles it, so a second flush skips it, and only the settle that set the mark clears it.

This PR's first review found that a storage change mid-settle could let go of the claim, and the settle's late end could then let go of a newer claim on the same target, stranding a message. Claims are now objects: a change while a claim's settle is out only notes the target, only that settle's end lets the claim go, and any late continuation can release its own claim and never another. The plan lists which test pins each rule.

A later review, after main moved, found two more gaps, both now fixed with tests that fail without the fix. First, targets settled one at a time, so one slow read held up the rest: they now settle together, as `handleReady` does. Second, a storage failure before the flush could list its targets was swallowed: it now tries again on the Board's backoff while the connection lasts. The same review's third finding, a race when the connection is replaced, can't happen; Step 4 says why.

---

## PR I: what you left behind sends itself

### Task 15: What you left behind sends itself

A message sent offline in a session you then left waits in the outbox with no screen to send it: only an open session screen registers its target (`createNativeMutationHost`, `nativeMutationHost.ts:57-108`). So does anything admitted for a session no screen holds, such as a review sent from a Reader opened from the Board (phase 4's `submitSessionMessage` settles only a registered target), or a Board Stop whose connection dropped before `BoardStops` let its target go (phase 2 part 3's Task 12.2). The web sends every target with waiting records on each ready connection (`handleReady`, `cmd/evener-hub/frontend/src/stores/threads.ts:2856-2921` on main at `d18386ade`, which settles them all at once). This task does the same, and also looks again whenever a record lands for a target nobody holds. It reuses phase 4's `settleTarget` (phase 4 Task 2), which releases a registered target with a read that leaves the connection's subscription alone. That also covers a session screen under the Reader or a subagent: it keeps its target but doesn't read while covered (phase 4 Task 2), so on a ready connection the flush settles its target for it when something on it waits to be sent.

A target nobody holds is settled whatever its records hold, as `handleReady` reads every stored target. The read is how the phone confirms a send whose answer was lost: a record the hub's read reflects is settled (`reconcileIdentities`), and one the hub proves it never received goes back to the outbox to send (`restoreProvenAbsent`, `mutationOutboxStorage.ts:492`). Spec 14 shows "Couldn't confirm this was sent" only when "delivery can't be confirmed after reconnecting".

**Files:**
- Create: `mobile-native/src/outbox/outboxFlush.ts`, `mobile-native/src/outbox/nativeOutboxFlush.ts` and `mobile-native/src/outbox/outboxFlush.test.ts`
- Modify: `mobile-native/src/nativeMutationRuntime.ts` (`targetClient`, beside `registerTarget`)
- Modify: `mobile-native/App.tsx` (bind the flush to the connection) and the session screen's durable-host effect (`screens.tsx:663-693`: flush after its host lets go)

**Interfaces:**
- Consumes: `NativeMutationRuntime.settleTarget(hubId, targetRef, client)` (phase 4 Task 2), `registerTarget`, `start`, `subscribeStorage`, `storage.listTargetRefs` and `storage.listOutbox`.
- Produces:
  - `NativeMutationRuntime.targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined`: the client that holds the target, or undefined
  - `parseTargetKey(key: string): { hubId: string; ref: string } | null`
  - `class OutboxFlush`: constructor `(runtime: () => FlushRuntime)`; `bind(hubId: string | null, client: AppwireClientLike | null)`, `flush(): Promise<void>`, `dispose()`
  - `outboxFlush`, the app's one instance, from `nativeOutboxFlush.ts`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/outbox/outboxFlush.test.ts
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

const message = (over: Partial<NativeMutationRequest> = {}): NativeMutationRequest => ({
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
		receipt: { clientMutationId, disposition: "applied", threadId: "thread-1", turnId: "turn-1", projectionState: "pending" },
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

it("reads the hub and ref out of a composite target key", () => {
	expect(parseTargetKey(JSON.stringify(["hub-1", "local:thread-1"]))).toEqual({ hubId: "hub-1", ref: "local:thread-1" });
	expect(parseTargetKey("local:thread-1")).toBeNull();
	expect(parseTargetKey(JSON.stringify(["hub-1"]))).toBeNull();
});
```

Each test pins one part of the flush, and fails without it:
- "sends a message kept while offline once the connection returns, even after a relaunch": the flush's `runtime.start()`, since a fresh runtime dispatches nothing until started (`#getClient`, `nativeMutationRuntime.ts:140-149`);
- "never takes a session a screen holds": the check on `targetClient`, so the flush never registers over a screen;
- "settles for a session screen that isn't reading, and leaves the target the screen's": settling a covered screen's target for it;
- "settles a covered screen's target once, however many flushes run beside each other" and "keeps a new connection's mark on a screen's target when an older settle finishes late": the in-flight mark, which only the settle that set it clears;
- "reads nothing for a session screen with nothing waiting to send": the `waiting` check before that settle;
- "sends what another screen admits for a session no screen has open": the storage watch;
- "keeps a message it can't settle, and lets its session go": letting go of a target whose settle comes back blocked, record kept;
- "lets go of a target it can't settle, and still sends the others": the per-target catch;
- "settles every target at once, so a read the hub is slow to answer holds up no other": settling the targets together, as `handleReady` does;
- "tries again after a backoff when storage fails before it can list what waits": the retry on the Board's backoff (`reconnectDelay`);
- "looks again for a record that landed while a settle it then lost was in flight": the `touched` set;
- "lets only a settle's own end release its claim, so a record that lands meanwhile still goes": a change while a settle is out only notes the target, and one read per settle;
- "leaves sending to the runtime: a record for a target it holds open goes without another settle": the division of labor, with one read for two sends;
- "never uses a read an older connection started, nor lets it touch the new one's": claims as objects, so a late continuation lets go of its own claim or none;
- "sends only the active hub's messages, and nothing while the connection isn't ready": the hub filter and the ready check;
- "lets every session go when the connection drops": `bind(null, null)` letting every claim go;
- "reads the hub and ref out of a composite target key": `parseTargetKey`.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/outbox/outboxFlush.test.ts`
Expected: FAIL: `Cannot find module './outboxFlush'`.

- [ ] **Step 3: Implement**

In `nativeMutationRuntime.ts`, before `beginAuthoritativeRead`:

```ts
	/** The client a session screen, or the flush, registered this target
	 * with; undefined while nobody holds it. */
	targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined {
		return this.#targets.get(nativeMutationTargetKey(hubId, targetRef))?.client;
	}
```

```ts
// mobile-native/src/outbox/outboxFlush.ts
// Sends what no open session is sending (spec 8.5's offline row, ruling 17):
// a message the phone kept while offline, in a session you have since left,
// goes out when the connection returns, as the web's handleReady does
// (cmd/evener-hub/frontend/src/stores/threads.ts); so does anything admitted
// for a session no screen holds, such as a review sent from a Reader opened
// from the Board. The flush takes only targets nobody has registered,
// settles each with a read that leaves the connection's subscription alone
// (NativeMutationRuntime.settleTarget), and lets each go once nothing on it
// is waiting to be sent.
// A session screen owns its own target; the flush only settles one that has
// something waiting, for a screen under the Reader or a subagent, which
// doesn't read while it's covered.
//
// The flush opens targets; the runtime sends. A settle's reconcile dispatches
// everything already waiting (reconcileAuthoritativeRead, dispatchTargets),
// and a record committed for a target that is open is discovered and sent on
// commit (MutationOutbox's announceCommit). So a record that lands during or
// after a settle goes either way; the flush only decides when to let a
// registration go.
import type { AppwireClientLike } from "@evener/appwire-client";
import { reconnectDelay } from "../hubConnection";

export type SettleResult = "open" | "reconciled" | "blocked" | "stale" | "unregistered";

/** The slice of NativeMutationRuntime the flush uses. */
export interface FlushRuntime {
	readonly storage: {
		listTargetRefs(): Promise<string[]>;
		listOutbox(targetRef: string): Promise<readonly { state: string }[]>;
	};
	start(): Promise<void>;
	targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined;
	registerTarget(hubId: string, targetRef: string, client: AppwireClientLike | null): () => void;
	settleTarget(hubId: string, targetRef: string, client: AppwireClientLike): Promise<SettleResult>;
	subscribeStorage(listener: (targetRefs: readonly string[]) => void): () => void;
}

/** The hub and ref a composite target key names (nativeMutationTargetKey),
 * or null for a key of any other shape. */
export function parseTargetKey(key: string): { hubId: string; ref: string } | null {
	try {
		const value: unknown = JSON.parse(key);
		if (Array.isArray(value) && value.length === 2 && typeof value[0] === "string" && typeof value[1] === "string")
			return { hubId: value[0], ref: value[1] };
	} catch {
		// Not a composite key: not the flush's to send.
	}
	return null;
}

/** The flush's hold on one target it is sending for. Each claim is its own
 * object, so a late continuation can only ever let go of its own. */
interface Claim {
	release(): void;
	/** Its settle is out: only that settle's end may let it go. */
	settling: boolean;
}

export class OutboxFlush {
	private hubId: string | null = null;
	private client: AppwireClientLike | null = null;
	private generation = 0;
	private readonly owned = new Map<string, Claim>();
	/** Claimed targets whose records changed while their settle was out. */
	private readonly touched = new Set<string>();
	/** Screens' targets this connection is settling for them right now. */
	private readonly settlingForScreens = new Map<string, symbol>();
	private unsubscribe: (() => void) | null = null;
	/** A look again after storage failed, and how many failed in a row. */
	private retry: ReturnType<typeof setTimeout> | null = null;
	private failures = 0;

	/** `runtime` is read at the first ready connection: a message kept from an
	 * earlier launch can only be found in the mutations database. */
	constructor(private readonly runtime: () => FlushRuntime) {}

	/** The active hub and its client while it is ready, else nulls. A new
	 * client flushes, and watches for work no screen will send; losing it lets
	 * every target go. */
	bind(hubId: string | null, client: AppwireClientLike | null): void {
		if (hubId === this.hubId && client === this.client) return;
		for (const claim of this.owned.values()) claim.release();
		this.owned.clear();
		this.touched.clear();
		this.settlingForScreens.clear();
		this.stopRetrying();
		// The runtime calls storage listeners synchronously, and unsubscribing
		// deletes this one at once, so no callback of an earlier bind runs after
		// this line. A check one started before holds the claim it found, and
		// release() lets go of that claim only.
		this.unsubscribe?.();
		this.unsubscribe = null;
		this.hubId = hubId;
		this.client = client;
		this.generation += 1;
		if (hubId === null || client === null) return;
		const runtime = this.runtime();
		this.unsubscribe = runtime.subscribeStorage((keys) => this.changed(runtime, keys));
		void this.flush().catch(() => undefined);
	}

	/** Looks again, for a session screen that let go of its target while the
	 * connection was live. */
	async flush(): Promise<void> {
		const { hubId, client, generation } = this;
		if (hubId === null || client === null || client.state !== "ready") return;
		const runtime = this.runtime();
		let keys: string[];
		try {
			// A fresh runtime dispatches nothing until started.
			await runtime.start();
			keys = await runtime.storage.listTargetRefs();
		} catch {
			// Storage failed before anything was claimed: look again after a
			// backoff while this connection lasts, as the Board retries a read.
			this.retryLater(generation);
			return;
		}
		if (generation !== this.generation) return;
		this.stopRetrying();
		// Every target settles at once, as the web's handleReady does, so a read
		// the hub is slow to answer holds up no other. A claim is taken before a
		// settle's first await, so a flush running beside this one finds the
		// target owned and skips it. Each settle catches its own failure, which
		// leaves that target for the next ready connection or the next record.
		await Promise.all(
			keys.map((key) => {
				const target = parseTargetKey(key);
				if (target === null || target.hubId !== hubId || this.owned.has(key)) return undefined;
				return this.settle(runtime, key, hubId, target.ref, client).catch(() => undefined);
			}),
		);
	}

	dispose(): void {
		this.bind(null, null);
	}

	private async settle(
		runtime: FlushRuntime,
		key: string,
		hubId: string,
		ref: string,
		client: AppwireClientLike,
	): Promise<void> {
		const holder = runtime.targetClient(hubId, ref);
		if (holder !== undefined) {
			// A session screen holds this target. Under the Reader or a subagent
			// it doesn't read, so after a reconnect its waiting message would
			// wait for a trip back: settle it for the screen, which keeps its
			// registration. Only a screen on this connection: a target held
			// with another client is that client's. A screen's target is never
			// the flush's to claim, so a mark keeps one settle at a time, and
			// only the settle that set it clears it (bind() clears them all).
			if (holder !== client || this.settlingForScreens.has(key)) return;
			const mark = Symbol(key);
			this.settlingForScreens.set(key, mark);
			try {
				if (await this.waiting(runtime, key)) await runtime.settleTarget(hubId, ref, client);
			} finally {
				if (this.settlingForScreens.get(key) === mark) this.settlingForScreens.delete(key);
			}
			return;
		}
		const claim: Claim = { release: runtime.registerTarget(hubId, ref, client), settling: true };
		this.owned.set(key, claim);
		this.touched.delete(key);
		let settled = false;
		try {
			// A read an older connection started can't land here: bind() lets
			// that connection's claims go, and the runtime answers a read for a
			// registration that is gone or replaced with "stale", before it
			// reconciles or dispatches anything (isCurrentRead).
			const answer = await runtime.settleTarget(hubId, ref, client);
			settled = answer === "reconciled" || answer === "open";
		} catch {
			// A read or storage failure: let go, as for a read that failed.
		}
		claim.settling = false;
		if (settled) await this.releaseIfDone(runtime, key, claim);
		else this.letGo(key, claim);
	}

	/** Lets go of a target the flush couldn't settle. A record that landed on
	 * it meanwhile was left to that settle, so look again for it; a failure
	 * with nothing new waits for the next record or connection, so a failing
	 * read never spins. */
	private letGo(key: string, claim: Claim): void {
		if (!this.release(key, claim)) return;
		if (this.touched.delete(key)) void this.flush().catch(() => undefined);
	}

	/** A target the flush holds may be done; a record for one nobody holds is
	 * work no screen will send. A claim whose settle is out only notes the
	 * change: that settle's end decides. A target a session screen holds stays
	 * that screen's here: its records come from the screen, or bring their own
	 * settle (a message sent from a screen above it), and settling it on every
	 * change would re-read after each unknown outcome and could resend in a
	 * loop. */
	private changed(runtime: FlushRuntime, keys: readonly string[]): void {
		let unclaimed = false;
		for (const key of keys) {
			const claim = this.owned.get(key);
			if (claim !== undefined) {
				if (claim.settling) this.touched.add(key);
				else void this.releaseIfDone(runtime, key, claim).catch(() => undefined);
				continue;
			}
			const target = parseTargetKey(key);
			if (target !== null && target.hubId === this.hubId && runtime.targetClient(target.hubId, target.ref) === undefined)
				unclaimed = true;
		}
		if (unclaimed) void this.flush().catch(() => undefined);
	}

	/** A target is done once nothing on it is waiting to be sent: what's left
	 * is settled, or waits for you in its session (a message it couldn't
	 * confirm, or one a Stop held). */
	private async releaseIfDone(runtime: FlushRuntime, key: string, claim: Claim): Promise<void> {
		if (!(await this.waiting(runtime, key))) this.release(key, claim);
	}

	/** Looks again after the Board's backoff (reconnectDelay: 1, 2, 4, 8 and
	 * 16 seconds, then every 30), while the connection the failed look ran on
	 * is still bound; bind() stops it. */
	private retryLater(generation: number): void {
		if (generation !== this.generation || this.retry !== null) return;
		this.failures += 1;
		this.retry = setTimeout(() => {
			this.retry = null;
			void this.flush().catch(() => undefined);
		}, reconnectDelay(this.failures));
	}

	private stopRetrying(): void {
		if (this.retry !== null) clearTimeout(this.retry);
		this.retry = null;
		this.failures = 0;
	}

	private async waiting(runtime: FlushRuntime, key: string): Promise<boolean> {
		const records = await runtime.storage.listOutbox(key);
		return records.some((record) => record.state === "submitting");
	}

	/** Lets go of this claim, and only this one: a late continuation of an
	 * older claim, or an older connection's, finds another claim or none, and
	 * leaves it be. True when it let go. */
	private release(key: string, claim: Claim): boolean {
		if (this.owned.get(key) !== claim) return false;
		this.owned.delete(key);
		claim.release();
		return true;
	}
}
```

```ts
// mobile-native/src/outbox/nativeOutboxFlush.ts
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import { OutboxFlush } from "./outboxFlush";

/** The app's one flush. App.tsx binds it to the connection; a session screen
 * asks it to look again when it lets go of its target. */
export const outboxFlush = new OutboxFlush(getNativeMutationRuntime);
```

- [ ] **Step 4: Wire it**
  - In `App.tsx`'s `Navigation`, bind the flush to the connection. `Navigation` already names its navigation state `state` (`App.tsx:67`), so take the connection's under another name: `const { client, state: connectionState } = useConnection();` beside the existing destructuring, then `useEffect(() => { outboxFlush.bind(activeProfile?.id ?? null, connectionState === "ready" ? client : null); }, [activeProfile?.id, connectionState, client]);`.
  - In the session screen's durable-host effect cleanup (`screens.tsx:689-692`), after `host.dispose()`, call `void outboxFlush.flush()`: letting go of a target writes nothing to storage, so without it a message still waiting when you leave a session would wait for the next connection.
  - Why a covered screen whose connection is replaced is never skipped: the screen's effect and App's bind both follow `useConnection()` (the screen's `connected` is `connectionState === "ready"` and its hub, `screens.tsx:641-642`), so they run in the same commit. The screen registers synchronously (`host.start()` calls `registerTarget` before its first await, `nativeMutationHost.ts:66-71`). The flush looks only after an await (`runtime.start()`), so when it checks ownership the screen already holds the target with the new client. Keep the bind in `Navigation`, and keep the flush's first check after an await.
  - Screen tests that import `screens.tsx` mock `./outbox/nativeOutboxFlush` to `{ outboxFlush: { flush: async () => {}, bind: () => {} } }`, the way they mock other native singletons.

- [ ] **Step 5: Run the tests and watch them pass**

Run: `cd mobile-native && npx vitest run src/outbox src/nativeMutationRuntime.test.ts src/ConversationScreen.offline.test.tsx && npm run check`
Expected: PASS. In the simulator: turn the network off, send in one session, go back to the Board, turn the network on, and see the session's turn start without opening it.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/outbox/outboxFlush.ts mobile-native/src/outbox/outboxFlush.test.ts mobile-native/src/outbox/nativeOutboxFlush.ts mobile-native/src/nativeMutationRuntime.ts mobile-native/App.tsx mobile-native/src/screens.tsx
git commit -m "feat(native): messages you left behind send themselves when the connection returns"
```

Open PR I: "feat(native): messages you left behind send themselves (phase 6, PR I)".
